# Tapas

Tapas is a local credential vault for coding agents and command-line tools. Enter
secrets in a browser form, store them encrypted with SOPS and age, and let a
command use them through environment variables. Agents work with credential
references, so you don't need to paste tokens into chat.

Use it directly from the terminal or through the included **Agent Secrets** skill
for Claude Code and Codex CLI.

## Install

Tapas supports macOS and Linux on arm64 and amd64. Install it with:

```sh
curl -fsSL https://raw.githubusercontent.com/Mosquid/tapas/main/install.sh | sh
```

The installer verifies downloaded checksums and installs Tapas and its SOPS
runtime dependency to `~/.local/bin`, without `sudo`. It leaves an existing SOPS
installation untouched. It also installs the skill for Claude Code and, if
`codex` is on your `PATH`, Codex CLI. No separate age CLI is needed.

Make sure the binary directory is on your `PATH`, then create your personal vault:

```sh
export PATH="$HOME/.local/bin:$PATH"
tapas version
tapas init --name personal
```

Add the `PATH` line to your shell configuration if needed for future sessions.
Initialization prints the path to your private age key. **Back up that key**:
you need it to decrypt the vault. Never commit it or share it in chat.

The personal vault is available from any working directory. See
[advanced usage](docs/usage.md) for custom locations and installer options.

## Use

### With a coding agent

Start a new agent session after installation. Ask it to use Tapas for a task
that needs a credential, for example:

> Use Tapas to authenticate with GitHub and list my repositories.

The skill guides the agent to find an existing credential or open a browser form
for you to add one, then run the command through `tapas run`. In Claude Code, you
can also invoke `/agent-secrets` directly. The command being run must be installed
separately; this example uses the GitHub CLI (`gh`).

### From the terminal

**1. Add a credential.** This example stores a GitHub token:

```sh
tapas add --name github --service github --env GH_TOKEN \
  --reason "List my GitHub repositories"
```

Enter the token in the browser and select **Save**. Keep the terminal command
running until you save or cancel. If the browser doesn't open, use the local URL
printed in the terminal. The form expires after five minutes.

**2. Find its reference.**

```sh
tapas list
```

Copy the credential reference, which looks like `store:personal/<id>`.
References stay the same when you rename a credential.

**3. Run a command with it.** Replace `CREDENTIAL_ID` below with the actual ID:

```sh
tapas run --ref store:personal/CREDENTIAL_ID -- gh repo list
```

Tapas supplies the token as `GH_TOKEN`, the variable chosen when adding it.
To choose a different variable, use `--ref VARIABLE=store:personal/CREDENTIAL_ID`.
Repeat `--ref` to supply multiple credentials.

### Manage credentials

| Task | Command |
| --- | --- |
| List metadata and references | `tapas list` |
| Edit metadata in the browser | `tapas edit --ref store:personal/CREDENTIAL_ID` |
| Replace a secret value | `tapas add --replace CREDENTIAL_ID` |
| Delete with browser confirmation | `tapas delete --ref store:personal/CREDENTIAL_ID` |
| Show commands and options | `tapas --help` |

## How secrets are handled

Only credential values are encrypted; names, descriptions, and other metadata
are readable. `tapas run` decrypts in memory and supplies credentials to the child
command's environment without changing your shell's environment.

Exact secret values are redacted from captured stdout and stderr, but the command
itself can access them. Redaction does not cover transformed values or output
written directly to files, terminals, or the network. Use trusted, non-interactive
commands. Tapas does not isolate secrets from other code running as your user.

See [advanced usage and security details](docs/usage.md) for storage behavior,
limited previews, and current limitations.

## Contribute

Bug reports and pull requests are welcome. Include reproduction steps and
expected behavior for bugs, using synthetic credentials in examples and logs.
For a larger change, open an issue first to discuss the approach.

To work locally, install the Go version specified in [go.mod](go.mod) and SOPS, then:

```sh
git clone https://github.com/Mosquid/tapas.git
cd tapas
go build -o bin/tapas ./cmd/tapas
go test -race ./...
go vet ./...
```

Run your build with `./bin/tapas`. Tests use temporary vaults and synthetic
credentials; they require SOPS and permission to bind local ports. Building and
testing do not create a personal vault.

The CLI lives in `cmd/tapas`; vault storage, browser forms, and command execution
live in `internal/vault`, `internal/entry`, and `internal/runner`. Agent instructions
live in `skills/agent-secrets`. Add relevant tests for behavior changes, update
the docs when usage changes, and include your verification results in the PR.
