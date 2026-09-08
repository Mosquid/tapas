# Agent Secrets — vault, temporary entry server, and credential runner

First implementation slice: a local SOPS vault, a browser process that saves one credential and exits, and a runner that delivers a stored credential to one child command. `tapas` is the provisional executable name.

The executable embeds the form and styles. Tagged releases provide macOS and
Linux binaries for arm64 and amd64, along with the Claude Code skill. npm
packaging and harness adapters remain later slices.

## Install

Install the latest release for the current platform without Node or `sudo`:

```sh
curl -fsSL https://raw.githubusercontent.com/Mosquid/tapas/main/install.sh | sh
```

The installer verifies the release archive against its SHA-256 manifest,
installs `tapas` to `~/.local/bin`, and installs the skill to
`~/.claude/skills/agent-secrets`. Both locations can be overridden, and a
specific release can be pinned:

```sh
curl -fsSL https://raw.githubusercontent.com/Mosquid/tapas/main/install.sh | \
  sh -s -- --version v0.1.0 --install-dir "$HOME/bin"
```

The installer never uses `sudo`. Ensure the selected binary directory is on
`PATH`, install SOPS, then initialize the vault:

```sh
tapas version
tapas init --name personal
```

### Install the Claude Code skill

The standard installer installs both the `tapas` executable and the Agent
Secrets skill. By default, the skill is written to:

```text
~/.claude/skills/agent-secrets/SKILL.md
```

Start a new Claude Code session after installation if `/agent-secrets` does
not appear among the available skills. Claude can invoke the skill
automatically when a task needs a credential, or you can invoke it directly:

```text
/agent-secrets
```

To install or update only the skill, without running the binary installer:

```sh
mkdir -p "$HOME/.claude/skills/agent-secrets"
curl -fsSL \
  https://raw.githubusercontent.com/Mosquid/tapas/main/skills/agent-secrets/SKILL.md \
  -o "$HOME/.claude/skills/agent-secrets/SKILL.md"
```

The skill expects `tapas` and SOPS to be available on `PATH`; installing the
skill alone does not install either executable or initialize a vault.

## Build and prerequisites

Requires Go 1.27.1 to build, plus `sops` on PATH at runtime. Currently tested with SOPS 3.8.1 on macOS arm64. The age library is compiled into the executable and creates identities; users do not install an `age` command.

```sh
go build -o bin/tapas ./cmd/tapas
```

`init` creates an age identity automatically. By default it lives in the OS user configuration directory, beside the personal vault. The output names its exact path because this recovery-critical private key should be backed up. Never paste it or provider secrets into chat.

## Initialize a vault

```sh
bin/tapas init --name personal
```

`--store` defaults to `vault.sops.json` inside the `tapas` directory of the OS user configuration directory, so one personal vault is reachable from any working directory. Pass `--store <path>` for a project vault instead. `init` creates a missing parent directory with owner-only permissions.

The personal vault sits in the same directory as the age identity. Their separation matters when the encrypted file travels: keep a project vault out of the directory that holds the key, and never commit the identity. A user who wants to sync an encrypted vault should place it elsewhere with `--store`.

Initialization generates the private identity with owner-only file permissions, encrypts an empty vault, and verifies decryption access before committing it. Existing identity and vault files are never overwritten. If vault initialization fails after generating an identity, that newly generated file is removed.

For an isolated preview that leaves normal user configuration untouched, put both artifacts under `/private/tmp`:

```sh
bin/tapas init \
  --store /private/tmp/tapas-demo-vault.sops.json \
  --identity /private/tmp/tapas-demo-identity.txt \
  --name demo

bin/tapas add \
  --store /private/tmp/tapas-demo-vault.sops.json \
  --identity /private/tmp/tapas-demo-identity.txt \
  --name github-development \
  --service github \
  --environment development \
  --env GITHUB_TOKEN \
  --reason "Authenticate GitHub commands"
```

The browser opens automatically. Keep the foreground command running until Save or Cancel. These demo paths may be cleared by the operating system; do not put real credentials in this disposable vault.

Advanced setup can use an existing identity by passing both its public recipient with `--age` and private file path with `--identity`.

This slice supports a strict JSON schema and exactly one age recipient. YAML, KMS, multiple recipients, and migration of arbitrary existing SOPS files are not implemented yet. Unsupported recipient policies are rejected rather than silently rewritten. A separate `.sops.yaml` is not required: the program supplies and preserves its explicit encryption policy.

## Add a credential

```sh
bin/tapas add \
  --name development-api \
  --service example \
  --environment development \
  --env API_TOKEN \
  --reason "Authenticate the development API client"
```

The foreground process opens the default browser and prints one JSON `awaiting_user` event with a fallback loopback URL and expiry. Use `--open=false` to open that URL yourself. The URL contains a short-lived form capability, never a provider credential.

Enter the value in the masked browser field. Save returns a final JSON `saved` event containing the actual reference, such as `store:personal/<id>`, and exits. Cancel, Ctrl-C, SIGTERM, or expiry also stops the server. The default expiry is five minutes; `--ttl 30s` shortens it. A save already in progress is allowed to reach its bounded completion before shutdown, so a committed save is not reported as cancelled.

Validation or encryption errors are shown in the browser without echoing the value. Use Back to correct input; for a revision conflict, cancel and reopen a fresh form. No secret value is accepted through CLI arguments, and there is no command that prints decrypted values.

## Use a credential

```sh
bin/tapas run --ref store:personal/CREDENTIAL_ID -- gh repo list
bin/tapas run --ref GH_TOKEN=store:personal/CREDENTIAL_ID -- ./deploy.sh
```

`run` decrypts in memory, places the value in the environment of exactly one child process, and returns that child's exit status. Bare `--ref REF` uses the credential's `suggested_env`; `--ref VARIABLE=REF` overrides it. Repeat `--ref` for several credentials; two credentials may not target the same variable. Values are never passed as process arguments, and the parent shell never receives them.

The runner removes exact occurrences of each delivered value from the child's stdout and stderr, across write boundaries. It does not decode transformed copies such as base64, and it does not see output the child writes directly to a terminal, a file, or a network destination. Because a short tail is held back to catch a split value, `run` suits non-interactive commands.

References resolve by ID only. A renamed entry keeps its reference, and a reused name never resolves to a different secret. There is still no command that prints a decrypted value.

## List and replace

```sh
bin/tapas list
bin/tapas add --replace CREDENTIAL_ID
```

`list` returns the logical store, encrypted-file revision, and credential metadata, without decrypting. Use the exact `id` from `list` for replacement. The form identifies the affected entry and requires a confirmation checkbox. The ID stays stable if the user renames the entry. Duplicate names cannot overwrite an existing entry through the addition flow.

## Storage and lifecycle guarantees

- Names, IDs, descriptions, service/environment labels, suggested variables, and recipient metadata are readable; only values are secret. Do not put secret material in metadata.
- Decryption and re-encryption use memory and private SOPS pipes. The store is decrypted on save, not kept decrypted while the form waits.
- Saves hold a cross-process lock and compare the form's original revision. Concurrent writers using this tool cannot lose updates. Any change since opening the form produces a conflict.
- Updates validate the newly encrypted document by decrypting it in memory, stage only encrypted bytes beside the destination, flush, and atomically replace the file. New files and staging files use owner-only permissions.
- A crash before replacement leaves the original intact; a crash after replacement may have saved successfully even if no final event reached the caller. Check `list` before retrying. A crash may leave an encrypted staging file; never a tool-created plaintext staging file.
- The `.lock` file deliberately remains so concurrent processes share one lock inode. Locks are released by the OS when a process dies. Non-cooperating external editors are outside the locking guarantee.
- The HTTP server binds to `127.0.0.1` on a random port. It validates token, Host, and Origin; rejects cross-origin writes and replay; limits request sizes; disables caching; and serves no external scripts or assets.
- The form has no JavaScript, analytics, browser storage, or request-body logging. Exact runtime-control environment variable names and prefixes are rejected by `vault.ValidateVariable`, both for suggested metadata and for `run` targets.
- `run` delivers values only to the environment of the single child process it starts. It does not modify the parent shell, other tool calls, or an already running server. Transparent harness hooks are not implemented in this slice.

This is not isolation from arbitrary code running as your user. RAM erasure is not guaranteed. Agent session claims, detached request/status persistence, transparent harness hooks, and native `CLAUDE_ENV_FILE` delivery remain future work; `run` is the explicit runner path. No personal vault or identity is created by building or testing the repository.

## Claude Code skill

`skills/agent-secrets/SKILL.md` instructs an agent to list before asking, to use exact references, to open the browser form for a missing credential, and to run credential-bearing commands through `tapas run`. The skill exists so that a session working on any project reaches for the vault instead of asking for a value in chat.

It therefore belongs in the personal skill directory, not in this repository's project scope:

```sh
ln -s "$PWD/skills/agent-secrets" ~/.claude/skills/agent-secrets
ln -s "$PWD/bin/tapas" ~/.local/bin/tapas
```

The symlinks keep one source file and one build. A rebuild of `bin/tapas` takes effect immediately. Sessions started before the link was created do not see the skill; restart them. A released package will install both; `tapas init` does not install them yet.

## Verify

```sh
go test -race ./...
go vet ./...
```

Tests require SOPS and permission to bind loopback ports. They create disposable age identities and synthetic credentials in temporary directories. Coverage includes encrypted round trips, rename/replacement, unchanged originals on failure, duplicate-key rejection, concurrent saves, HTTP token/Host/Origin checks, escaped metadata, replay rejection, cancellation, expiry, listener shutdown, reference resolution by ID, split-write redaction, child environment delivery, and child exit status. Browser layout has not yet been manually verified in a graphical browser.

In a restricted development sandbox, point `GOCACHE` and `GOPATH` at writable directories if needed. This does not affect the installed binary.
