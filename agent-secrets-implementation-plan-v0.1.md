# Agent Secrets — implementation plan v0.1

Status: original plan with an implementation-order amendment. Based on `agent-secrets-spec-v0.1.md` and the clarification that this is an installable product distributed through npm and `curl | sh`.

Implementation-order amendment: the user subsequently chose to start with the local SOPS vault and temporary decrypt/update/encrypt server. That slice now exists (see `README.md`), initially for JSON and one age recipient. Adapter probes and release packaging remain pending; no harness compatibility is claimed. The original gates below describe the remaining product plan rather than blocking this explicitly requested first slice.

## 1. Product and distribution decisions

Users install a released package; they do not clone this repository, compile the tool, or maintain custom scripts. The installed CLI includes the agent skills, hook adapters, and browser assets. It runs locally and connects to a project through `init`.

Recommended implementation: a Go executable with embedded assets, a thin npm launcher, and a POSIX shell installer. Both installation channels deliver the same executable for each platform. This makes the curl installation independent of Node and keeps execution, browser handling, and state management in one implementation. Confirm this choice after the adapter spike, before building the rest of the product.

Initial build targets: macOS and Linux, arm64 and amd64. Claim support only for combinations exercised in release tests. Pin a supported Go toolchain in development/CI; the old Go installation on this development machine is not a product constraint.

Distribution design:

- npm: one public entry package with a `bin` launcher and exact-version platform packages selected through `optionalDependencies` and `os`/`cpu`. Ship binaries in those packages, avoiding install-time downloads and compilation. Missing platform dependencies produce a repair instruction. Verify signal and exit-status forwarding through the launcher.
- curl: a hosted `install.sh` detects platform/architecture, selects a released version, downloads its archive and checksum manifest, verifies integrity, and installs to a user-owned location. Support version pinning and an explicit install directory. Stage installation before switching the executable; an interrupted download must preserve the working version. Never implicitly use sudo.
- Embed templates and browser assets into the executable. Hooks use a stable installed path, never a source-checkout path or temporary `npx` cache path.
- Keep package versions, binary versions, and adapter asset versions aligned. Verify all platform artifacts before publishing the entry package or moving the stable installer pointer.
- npm upgrades use npm; curl upgrades re-run the installer. Detect conflicting installations in `doctor`. Uninstall removes owned program/integration files while preserving the encrypted store and user keys.
- Installation puts the program on the machine. `init` separately configures a project and merges the selected harness integration. Package installation must not inspect keys or modify projects.

SOPS remains an explicit external prerequisite for v0.1, matching the specification. `doctor`/`init` explain how to install it and configure an existing decryption identity. Do not silently generate keys or install another package manager. Final package name, npm scope, release host, and license remain publishing decisions; use placeholders until selected.

## 2. Gate A: prove adapters and installation shape

Build only a disposable synthetic-secret runner and minimal adapter probes first. Do not build the vault, browser workflow, or production registry until these probes pass.

Record exact harness versions, OS, shell, tool input shapes, hook ordering, approval behavior, and output paths. Locally observed versions are Codex CLI 0.153.0, Claude Code 2.1.263, and SOPS 3.8.1; these are test candidates, not a support claim.

| Probe | Required evidence / decision |
| --- | --- |
| Explicit runner in both CLIs | Child receives a synthetic value; arguments and returned output do not expose it; cwd, exit status, stdin, cancellation, and shell behavior survive. |
| Codex transparent wrapping | Supported shell input can be rewritten without changing approvals/sandbox policy or executing twice. Test hook failures, competing hooks, and recursive wrapping. If preservation cannot be demonstrated, ship explicit runner mode. |
| Claude native bootstrap | Environment file contains only references/bootstrap code. Prove the bootstrap executes for each supported command, immediately sees claims, and clears released bindings. Check harness-generated environment snapshots for plaintext too. |
| Claude readiness | Bounded acknowledgement identifies the installed bootstrap revision. Demonstrate no deadlock from a claim waiting on a hook that only runs after the calling tool returns. If needed return pending and poll; only acknowledged claims become ready. |
| Session identity | Derive a fresh session incarnation and canonical project identity from integration state. Prove isolation across two concurrent sessions and conversation resume. If hooks cannot propagate context, test a packaged harness launcher as the explicit fallback. |
| Output exposure | Inspect streaming output, tool results, transcripts, and controlled outbound model requests. Native mode must declare lack of redaction unless interception is actually demonstrated. |
| Package smoke test | Install a prototype npm tarball and a prototype binary archive outside the checkout; embedded assets, child handling, and adapter paths still work. |

Decision record: choose default mode for each CLI, exact supported command set, session transport, shell/TTY limitations, and runtime language. The runner is the baseline for Codex and the specified fallback for Claude. Prefer runner mode for Claude if native mode cannot meet the leakage acceptance criteria. Transparent integration is optional; both CLI adapters are required.

Official docs establish Codex `PreToolUse` rewriting, but do not by themselves prove approval preservation for this wrapper. That remains an experiment. [Codex hooks](https://learn.chatgpt.com/docs/hooks)

Claude documents `CLAUDE_ENV_FILE` for SessionStart, Setup, CwdChanged, and FileChanged. Whether a dynamic bootstrap survives its execution lifecycle without plaintext snapshots remains an experiment. [Claude hooks](https://code.claude.com/docs/en/hooks)

## 3. Freeze contracts after Gate A

### Store and references

- One explicit logical store name and configured path per canonical project root; no parent-directory or unrelated-store discovery.
- Versioned JSON and YAML representations of the same strict schema: credential map keyed by immutable ID, editable name/description/service/environment/suggested variable, and encrypted string value. Reference format: `store:<logical-name>/<credential-id>`.
- A rename changes the display name, not an existing ID. Addition allocates the final ID on save and returns the actual saved reference; callers never reconstruct it from suggested metadata.
- Explicitly allowlist plaintext metadata in SOPS configuration; reject unknown schema fields, duplicate keys, unsafe YAML tags, and unexpected plaintext values. Discovery parses ciphertext structure without invoking decryption. Execution verifies integrity through SOPS.
- Reject unsupported existing schemas with mapping/migration guidance. No automatic destructive migration.
- Proposed simple v0.1 revision rule: hash the complete encrypted store snapshot. Any store change invalidates prior claims, including unrelated additions. This conservatively satisfies replacement invalidation, with a documented re-claim cost. Defer per-credential revision optimization.
- Environment values must be nonempty strings without NUL; define size limits and multiline behavior. Variable names need both syntax validation and a documented runtime-control deny policy, including loader, shell startup, interpreter injection, and tool-internal controls.

### Session state and operations

- Private per-user state directories with owner-only permissions; persist only references, revision markers, request outcomes, and other non-secret metadata. Keep project configuration separate from ephemeral session state.
- Bind registry access to validated integration context, project root, and a fresh session incarnation. A supplied session ID alone is insufficient. Treat this as accidental-isolation protection, not a boundary against arbitrary code running as the same user.
- Claims transition through pending, ready, invalidated, and released. Repeated equivalent claims are idempotent; conflicting variable mappings are errors. Reject invalidated claims before child launch.
- Addition requests transition from awaiting-user to saving and then saved/failed; cancellation and expiry are terminal when saving has not begun. Define the commit point so a completed save cannot subsequently be labelled cancelled or expired.
- Use versioned JSON responses for control operations, with stable error codes and retryability. Separate missing entry, decryption/authentication failure, revision conflict, adapter failure, and cancellation.
- `run` preserves streaming stdout/stderr and child exit status; offer a structured event mode if required. Keep control JSON off the child output stream. No agent-facing secret-value arguments or decrypt/export command.
- Specify the release race: a command that has already acquired its execution snapshot may finish; commands acquiring a snapshot after release cannot use the binding. Existing children are unaffected.
- Track variables managed during this session. Remove those variables from the child environment before applying the selected active bindings, including released and unselected mappings. This prevents stale or ambient credentials from reappearing under a previously managed name.

## 4. Existing-secret vertical slice

Implement in this order:

1. CLI command/response contracts, config loading, structured safe errors, and `doctor`.
2. `init`: validate executable versions, schema, SOPS recipients and decryption access; install bundled skill and chosen adapter through non-destructive config merging. Record owned integration entries and handle repeated init. Explain when the initial harness setup requires a new session.
3. SOPS subprocess boundary: use private stdin/stdout pipes and explicit formats/configuration. Never forward raw decrypted stdout or unsanitized subprocess stderr. Use the configured recipient policy and prevent accidental recipient changes during saves.
4. `discover`: deterministic metadata search and filtering; return all plausible account/environment candidates without privileging production.
5. Session registry, locking, idempotent `claim`, `status`, `release`, revision checks, and adapter acknowledgement.
6. `run`: validate selected claims, resolve one consistent encrypted snapshot, decrypt locally, build the child environment, and start only after all required claims resolve. Preserve the original shell and flags for shell commands; pass argv directly for direct execution.
7. Streaming exact-value redaction on both output streams. Retain sufficient undecided bytes across reads, handle overlapping values and EOF, and apply backpressure. Forward cancellation to the child process group and reap descendants. Document transformed values, external logs, arbitrary child files, and TTY limitations.

SOPS supports pipe-based encryption/decryption; actual command flags must be verified against the supported SOPS version, particularly the locally installed older version. [SOPS advanced usage](https://getsops.io/docs/usage/advanced/)

Exit gate: from an installed package, both CLIs discover and claim an existing synthetic credential, execute an authenticated HTTP fixture, and release it without restarting the conversation. Test unavailable decryption, two sessions, conflicting variables, invalidated revisions, and immediate next-command delivery.

## 5. Missing-secret vertical slice

1. `request-add` starts a short-lived helper through the installed executable. Pass metadata through a private channel; obtain a listening/ready acknowledgement before returning request ID, expiry, browser-open result, and fallback URL.
2. Serve embedded assets on numeric loopback with a random port. Use a random short-lived capability, exact Host/Origin checks, POST token validation, request/body limits, no request logging, no cache, no external resources, and escaped metadata. Protect Cancel as well as Save. Do not put credentials in URLs or browser storage.
3. Implement the masked form, editable metadata, clear purpose, Save/Cancel, and explicit replacement confirmation naming the target. Test keyboard operation and readable errors.
4. Under a stable store lock, re-read the current revision and validate duplicate/replacement rules. Decrypt in memory, apply the change, encrypt through private pipes, validate ciphertext, and atomically replace using a same-directory encrypted temporary file and durability flushes. No plaintext temp files.
5. Coordinate participating writers using the lock; detect changed revisions from outside writers and refuse overwrite. Document that arbitrary non-cooperating editors cannot participate in the tool's locking guarantee.
6. Persist a non-secret commit intent before replacement and enough commit identity in the encrypted document to reconcile a crash between store replacement and request-result recording. Confirm saved only after durable commit; recover ambiguous outcomes on the next invocation.
7. Return the final saved reference. Saving never claims automatically. Poll status with bounded backoff; cancellation/expiry stop automatic retries. A failed claim after save must preserve and report the saved entry.
8. Shut down after response delivery, cancellation, or expiry; reject replay and parallel submissions. Recover stale helper/request state after crashes without killing unrelated processes.
9. Add an agent-facing `preview` operation that resolves an exact reference and returns metadata, character count, and a strictly bounded fragment. Prioritize format-recognizable prefixes while revealing no more than one quarter of the value and eight characters. Test short values, Unicode character counts, disclosure limits, and absence of the full value from output.

Exit gate: install → init → missing discovery → browser save with a user rename → poll → claim the final reference → authenticated command. Also pass overwrite, duplicate submission, cancellation, expiry, concurrent save, disk failure, and crash recovery tests.

## 6. Packaging, compatibility, and release gate

Organize the implementation around `cmd/`, `internal/{config,store,session,runner,redaction,requests,adapters}`, embedded `assets/`, `packaging/npm/`, `install/`, and `tests/`. Avoid a permanent service or an MCP dependency.

Deliver a versioned support matrix and test from packaged artifacts:

| Area | Required coverage |
| --- | --- |
| Installation | npm global install and curl install on clean supported macOS/Linux machines; no checkout/compiler; curl without Node; paths with spaces; unsupported platform; failed download/checksum; package scripts disabled. |
| Lifecycle | Repeated init, pre-existing hooks, upgrade, adapter-version mismatch, uninstall ownership, preserved stores/keys, and resumed sessions starting unclaimed. |
| Execution | Direct argv and shell strings, quoting, pipelines, redirection, cwd, stdin, nonzero exits, streaming/backpressure, cancellation, concurrent commands, approvals, and sandbox-denied execution. |
| Storage | JSON/YAML fixtures, strict schema, plaintext metadata policy, SOPS unavailable, identity/KMS failure, recipient preservation, replacement invalidation, locking, and failure injection around each commit step. |
| Browser | Token/Host/Origin rejection, escaped metadata, oversized body, no-cache headers, explicit overwrite, rename, replay, cancellation, timeout, browser-open failure, and helper termination. |
| Leakage | Synthetic values absent from tool-owned errors, args, state, temp artifacts, tool results, and transcripts; chunk-boundary/overlap redaction; controlled outbound completion inspection for each supported harness mode. |

Use a local HTTP service and an actual database CLI against an isolated database fixture, both checking synthetic credentials without echoing them. Compare fixture acceptance with transcript/output scans; a green command alone is not leakage evidence. If outbound inspection is unavailable for a harness, record that acceptance criterion as unverified.

Release work includes versioned archives, checksums, npm packages, installer hosting, concise setup/recovery docs, the support matrix, and a short end-to-end demo. Verify the archives and npm packages are built from the same release source. Checksums establish artifact integrity relative to the manifest; they do not independently authenticate a compromised distribution host.

npm supports executable entry points, optional dependencies, and platform restrictions needed for the proposed package layout. [npm package.json reference](https://docs.npmjs.com/cli/v11/configuring-npm/package-json/)

## 7. Concrete work order

1. Adapter and package prototype; record pass/fail evidence and choose modes/runtime.
2. Freeze schema, response contracts, session transport, revision semantics, and environment policy.
3. Establish the binary build and npm/curl artifact skeleton so subsequent slices are tested as installed packages.
4. Ship the existing-secret slice internally, including redaction and release.
5. Add the browser/save/continuation slice with concurrency and crash recovery.
6. Complete skill guidance, integration lifecycle, compatibility tests, and leakage verification.
7. Prepare release artifacts and installation docs, then evaluate installation and real task completion with outside developers. No automatic telemetry.

Product naming and publishing-account choices can wait while the implementation proceeds; publishing itself requires those choices and release authorization. See the implementation-order amendment above for current progress.
