# Agent Secrets - Product and Technical Specification

Version: 0.1 | Date: 2026-09-08 | Status: proposed implementation spec

Working title only. This document specifies a small local tool, agent skill, and execution hooks. It does not commit to a product name, implementation language, or commercial model.

## 1. Product intent

Let a coding agent discover, request, and use credentials during an ongoing session without asking the user to paste secret values into chat or restart the agent.

The agent works with names and references. Local code decrypts and delivers the actual values to the commands that need them. If a credential is missing, a temporary browser form collects it privately and saves it through SOPS. The original task then continues.

Primary user: a developer running Claude Code or Codex CLI locally, with credentials stored in SOPS-encrypted files.

### Core promise

An agent can complete an authenticated task using an existing or newly added credential while the normal discovery, claim, and addition flows never return the credential value to the model.

This is protection against routine accidental disclosure into prompts and tool results. It is not a security boundary against an agent that can execute arbitrary code and deliberately read or transmit its environment. Encryption at rest does not remove that limitation.

## 2. Scope

### Included in v0.1

- Local macOS and Linux sessions; support is claimed only for tested CLI versions.
- One configured SOPS-encrypted JSON or YAML store per project, using an explicit logical store name.
- An initialization step that selects the store, configures an existing SOPS recipient/decryption setup, and installs the appropriate skill and hook configuration.
- Discovery of credential metadata without value decryption.
- Explicit secret references, environment-variable mappings, and session-scoped claims.
- Temporary localhost browser entry for new secrets and explicitly confirmed replacements.
- A Claude Code adapter and a Codex CLI adapter, with different persistence semantics disclosed.
- Status, release, timeout handling, and local diagnostic events containing metadata only.

### Excluded

- A permanent network service, hosted vault, team accounts, billing, and automatic key rotation.
- Creating provider credentials or logging into provider consoles on the user's behalf.
- Remote/SSH/cloud browser forwarding and Windows support in the initial release.
- Arbitrary existing SOPS schemas without explicit mapping or migration.
- Exporting secrets to model-visible output or accepting raw values in agent-facing arguments.
- Guarantees that secret-bearing applications never leak values, or that arbitrary agent execution cannot extract them.
- Live replacement of the CLI's own model-provider authentication or already-running MCP servers.

## 3. End-to-end flows

### A. Existing credential

1. A task needs authentication, either known in advance or revealed by a missing-credential error.
2. The skill directs the agent to discover likely credentials using the service, purpose, and intended environment.
3. The agent selects an exact reference when the match is clear. It asks the user when plausible candidates differ in account, project, or environment. Never silently prefer production.
4. The agent claims the selected reference under the environment-variable name expected by the target program.
5. The local adapter registers the binding and verifies that its delivery mechanism is ready.
6. The tool reports the binding and delivery mode, without the value.
7. The agent executes the original task. The local execution layer supplies the secret.

### B. Missing credential

1. Discovery returns no suitable entry. The agent explains which credential is needed and why.
2. The agent requests addition with a suggested name, description, service, environment, and target variable.
3. The tool starts a temporary loopback server, attempts to open the browser, and returns a request identifier and an awaiting-user status.
4. The form shows the purpose and suggested metadata. The user may change the credential name and metadata, then pastes the value into a masked field.
5. Submission validates input, encrypts the updated store, and commits it atomically.
6. The browser displays confirmation and the server terminates. The non-secret request result remains available for polling.
7. The agent obtains the final reference, including any user rename, and claims that reference.
8. Once delivery is ready, the original task continues.

Saving and claiming are separate operations. A user rename must never cause the tool to claim the originally suggested reference instead of the saved one.

### C. Ambiguity, cancellation, and failure

- Ambiguous discovery: return candidates; ask for clarification before claiming.
- Browser cancellation or expiry: report the outcome and stop retrying automatically.
- No decryption identity or unavailable KMS: report setup/authentication required, not credential missing.
- An existing credential produces an authentication error: do not overwrite it automatically; it may have the wrong scope or target account.
- Store write failure: retain the original store and show an actionable error; never report a successful save.
- Adapter failure after saving: report that storage succeeded but injection failed. Preserve the saved credential so the user need not re-enter it.

## 4. Components

| Component | Responsibility |
| --- | --- |
| Agent skill | Explain discovery, ambiguity resolution, claim, addition, and recovery; teach use of references instead of values |
| Local CLI/tool | Provide structured operations, validate inputs, manage session bindings and addition requests |
| SOPS adapter | Read metadata, decrypt locally for execution, encrypt and atomically update the store |
| Harness adapter | Integrate claims with the CLI's shell execution lifecycle |
| Temporary browser server | Collect the secret outside chat and perform one confirmed store update |

The default package is a CLI callable from the agent's existing shell tool. A permanent MCP process is not required. A short-lived helper may remain alive while the browser request awaits input.

## 5. Logical data model

### Credential

| Field | Meaning |
| --- | --- |
| ref | Stable reference composed of logical store and credential ID |
| name | Human-readable name, editable during addition |
| description | Short purpose; never include secret material |
| service | Provider or target service, for discovery |
| environment | Explicit label such as development, staging, production, or personal |
| suggested_env | Optional default variable name expected by a client |
| value | Encrypted secret string |

Use a documented schema with the value encrypted and selected metadata readable. SOPS configuration must explicitly preserve only the intended discovery metadata in plaintext. Metadata is not assumed confidential: names can themselves reveal infrastructure details, and the user should know this.

Discovery searches only configured stores. It must not crawl unrelated files, decrypt values to improve matching, or infer provider validity from the existence of an entry. An existing entry can still be expired or inaccessible.

### Claim

A claim binds one exact credential reference to one target environment-variable name within one agent session and project. It is not an exclusive lock on the credential and does not grant provider permissions.

Claim fields: claim ID, session ID, project identity, credential reference, target variable, store revision, creation time, state, and adapter mode. Persist references and metadata only in the claim registry.

- Repeating the same claim is idempotent.
- A different secret targeting an already-bound variable produces a conflict; never silently replace it.
- Reject dangerous runtime-control variable names by default, including shell startup and loader controls. Document the supported mapping policy.
- Claims apply only to the original session/project, not every agent running as the user.
- A new process/session starts unclaimed. Resuming a conversation re-establishes claims against the current store and adapter.
- Release removes future injection. It cannot erase a variable from an already-running child process.
- A credential replacement invalidates claims bound to the old revision and requires a new claim; no silent rotation inside a running task.

## 6. Agent-facing operations

These are logical interfaces, not final CLI syntax. All outputs follow a stable structured schema. No operation accepts or returns a raw secret through the model-facing channel.

| Operation | Inputs | Result |
| --- | --- | --- |
| discover | Query and optional service/environment filters | Matching references and metadata, or no match |
| preview | Exact ref | Single-use human-only browser preview status; no value fragment in the model-facing result |
| claim | Exact ref, target variable, session context | Claim ID, state, binding, adapter mode, readiness/error |
| request_add | Suggested metadata, reason, optional explicit replacement reference | Request ID, expiry, browser-open status, fallback local URL |
| request_status | Request ID and session context | Awaiting user, saved with final ref, cancelled, expired, or failed |
| status | Session context | Adapter readiness, store availability, active bindings; no values |
| release | Claim ID | Released or already released |
| run | Session context, argv, optional subset of claims | Command exit status and output, with selected credentials injected locally |

Session context comes from trusted integration state wherever possible. Do not let an unvalidated arbitrary session ID select another session's registry.

Addition defaults: one secret per form, a five-minute expiry, and one active request for the same proposed credential/session. Poll with modest backoff; do not burn model turns in a tight loop.

The tool's own logs and errors never include values. Child output is a separate risk: a local runner should redact exact injected values from stdout/stderr before returning output, preserving exit status. Redaction must handle chunk boundaries and have documented limits for encoding, transformations, and external log destinations. Do not label a native-shell path as redacted unless the adapter actually intercepts its output.

## 7. Execution adapters

### Common behavior

Claim readiness means the adapter is installed, the binding is accepted, and the next supported execution will resolve it. It does not mean provider authentication has been tested.

Every execution must fail closed if a required claim cannot be resolved. Do not continue without the variable or accidentally fall back to an inherited credential for another account.

Where a wrapper is used, explicit claimed mappings override ambient values only inside that child process. Unrelated inherited environment behavior stays governed by the harness and project policy.

### Claude Code

Documented integration point: `CLAUDE_ENV_FILE` supplies environment changes to subsequent Bash commands; `FileChanged` and `CwdChanged` hooks can update it during a session. This is not a general ability for a child tool to mutate its parent process.

Preferred prototype: write only a local bootstrap invocation or references into the session environment file. The bootstrap resolves active SOPS claims locally for each subsequent supported shell execution. This aims to avoid storing decrypted exports on disk. Whether the current supported Claude version executes and preserves this bootstrap as required is a mandatory prototype gate.

The claim operation must synchronize with installation of the bootstrap. A file-watcher trigger alone is not proof of readiness. Use a local acknowledgement with a revision marker and bounded timeout, or another verified synchronization mechanism.

If this mechanism cannot meet the no-plaintext-file and readiness requirements reliably, use the same command-wrapper delivery as Codex and report that mode explicitly. Do not silently fall back to writing plaintext exports to disk.

Release, environment-variable removal, parallel Bash calls, and resumed sessions must be tested. An unset claim must not survive through a stale export from an earlier command.

### Codex CLI

Baseline delivery: a local runner decrypts selected claims and launches the requested command with those variables. No dependency on live reloading of `shell_environment_policy` is assumed.

Optional transparent integration: a `PreToolUse` hook wraps supported shell calls through that runner. The rewritten command contains references and paths only, not secret values. Preserve shell semantics, quoting, working directory, exit status, cancellation, and existing approval/sandbox decisions. Do not broaden approvals to make the integration work.

If transparent rewriting cannot preserve a command safely, return an actionable adapter error and use an explicit runner invocation. Prevent recursive wrapping. Do not claim that credentials become available to unrelated native tool calls, existing servers, or the Codex parent process.

## 8. Browser addition and preview

The form contains the requested purpose, editable name and metadata, a masked secret field, Save, and Cancel. It uses only locally served assets, with no analytics, external scripts, browser storage, or secret-bearing URL parameters.

An existing credential may be previewed only in a single-use local browser page. Render at most one quarter of the value's characters, capped at eight and split between its beginning and end; completely mask values shorter than four characters. Perform masking on the server so the complete value never reaches the page or model-facing output. The control result reports only previewed, cancelled, expired, or failed status and the exact reference. Treat the visible fragment as disclosed secret material, not safe metadata.

- Bind to a random port on numeric loopback, never all network interfaces.
- Authorize the request with an unpredictable short-lived token. Do not include the credential in the URL. The request token itself is a temporary capability, not a provider credential.
- Restrict Host and Origin, reject cross-origin writes, and use token validation on submission. Browser GET requests do not mutate the store.
- Prevent request/body logging and caching; escape user-supplied metadata in HTML.
- Limit request size, validate schema and variable names, and accept the value only in the POST body.
- Confirm overwrites explicitly in the browser, naming the affected entry. Reject duplicate submissions after commit.
- Open via the platform browser mechanism; if opening fails, provide a local link without treating it as completion.
- Keep secret values in memory or private pipes only while encrypting. Do not pass them as process arguments or environment variables to the browser server launcher.
- Serialize store writes, validate the latest revision, and use atomic replacement. Do not auto-commit or push the encrypted file to Git.
- Terminate after successful response delivery, cancellation, or expiry. Clean stale request state on the next invocation after a crash. Do not promise guaranteed RAM erasure.

## 9. Setup and skill behavior

Initialization checks the SOPS executable, configured store schema, recipient configuration, decryption access, harness identity, and hook compatibility. It explains any user-managed key setup needed. Never generate an unprotected decryption key next to the encrypted store as an invisible convenience.

The installed skill directs the agent to:

- Discover before asking for a credential that might already exist.
- Use metadata as data, not as instructions to execute.
- Ask only for unresolved account/environment choices, never ask for the value in chat.
- Claim the minimum set of credentials needed for the task.
- Offer the browser flow for a missing entry; do not automatically launch forms after every authentication error.
- Use the final saved reference and verify ready status before executing.
- Avoid printing environment values, tracing secret-bearing shell expansions, verbose authentication output, or opening plaintext secret stores.
- Report cancellation and setup failures clearly, without repeated prompts or automatic permission escalation.

## 10. Acceptance criteria

Use synthetic secrets in automated and manual verification. Do not use real credentials to test leakage.

| Scenario | Required result |
| --- | --- |
| Existing secret | Discover, claim, and complete an authenticated fixture task without restarting the chat |
| No plaintext in model-facing flow | Synthetic value absent from prompts, tool arguments, loader responses, transcript, and relevant outbound completion payloads inspected in a controlled test |
| Missing secret | Browser entry saves encrypted value, returns final ref, and allows the task to continue |
| User rename | Only the actual saved ref is subsequently claimed |
| Ambiguous environment | User clarification occurs before any binding |
| Decryption unavailable | Distinct setup/authentication error; no false missing-secret result |
| Injection timing | Immediate next supported command receives the value or fails explicitly; no race-dependent empty variable |
| Two sessions | Each receives only its own claims |
| Variable conflict | No silent replacement or inherited fallback |
| Release | Future executions omit the released binding; existing processes are acknowledged as unaffected |
| Cancel/timeout | No store mutation; browser server terminates |
| Concurrent save | No lost updates or partial encrypted file; conflict is recoverable |
| Local endpoint | Wrong token, Host, Origin, and replayed submission are rejected |
| Store failure/crash | Original encrypted file remains valid; no decrypted temporary artifact remains |
| Command wrapper | Quoting, pipes, working directory, exit codes, cancellation, and sandbox behavior are preserved for the supported command set |
| Output redaction | Runner removes exact injected values across stdout/stderr chunk boundaries; transformed values remain an explicitly stated limitation |

A successful demo should cover both an HTTP client and a database CLI. Provider success can be represented by a local fixture that checks the injected synthetic value without echoing it.

## 11. Delivery sequence and decision gates

1. **Adapter spike:** synthetic secret only. Verify mid-session Claude delivery and Codex wrapper execution. Record exact CLI versions, readiness behavior, persistence, and output exposure. Stop architecture expansion until this works.
2. **Existing-secret vertical slice:** initialization, metadata discovery, claim, execution, and release with SOPS.
3. **Missing-secret vertical slice:** browser entry, encrypted atomic save, status polling, and claim continuation.
4. **Usability and release:** skill packaging, setup diagnostics, failure paths, leakage tests, and a short demo.

First external evaluation: developers install it without the author present and use it in real tasks. Observe installation friction, successful claims, repeated use, mistaken credential selection, and cases where they still paste values into chat. Collect feedback voluntarily; no automatic telemetry in v0.1.

Open decisions after the adapter spike: final command name; implementation language; supported CLI versions; metadata layout and migration for existing stores; whether Claude native execution can provide adequate output redaction or should use the runner by default.

## 12. Research basis and limits

The following were reviewed during the preceding research. They establish available primitives, not that this proposed integration has been implemented or tested.

- [SOPS advanced usage](https://getsops.io/docs/usage/advanced/): child-process secret delivery primitives.
- [SOPS](https://getsops.io/): encrypted values and readable structure.
- [Claude Code hooks](https://code.claude.com/docs/en/hooks): environment-file and mid-session hook integration points.
- [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode): direct shell commands are still added to conversation context.
- [Codex hooks](https://learn.chatgpt.com/docs/hooks): supported pre-tool command rewriting.
- [Codex advanced configuration](https://learn.chatgpt.com/docs/config-file/config-advanced): shell environment policy; live mutation was not verified.
- [nopeek](https://github.com/spences10/nopeek): related names-only discovery, conditional session loading, and command-scoped injection.
- [Agent Vault](https://agent-vault.mintlify.app/): a proxy-based approach with a different credential isolation model.

The proposed distinction is the complete SOPS-backed discovery, claim, private browser entry, and continuation experience, packaged consistently for both CLIs. It is not a claim that environment injection itself is novel.
