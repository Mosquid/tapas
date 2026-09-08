# Agent Secrets — vault and temporary entry server

First implementation slice: a local SOPS vault and a browser process that saves one credential, then exits. `tapas` is the provisional executable name.

The executable embeds the form and styles. The intended product distribution remains npm and `curl | sh`; release packaging and harness adapters are later slices. These commands are for developing and trying the current implementation.

## Build and prerequisites

Requires Go 1.27.1 to build, plus `sops` on PATH at runtime. Currently tested with SOPS 3.8.1 on macOS arm64. The age library is compiled into the executable and creates identities; users do not install an `age` command.

```sh
go build -o bin/tapas ./cmd/tapas
```

`init` creates an age identity automatically. By default it lives in the OS user configuration directory, separate from the project and encrypted vault. The output names its exact path because this recovery-critical private key should be backed up. Never paste it or provider secrets into chat.

## Initialize a vault

```sh
bin/tapas init --store vault.sops.json --name personal
```

Initialization generates the private identity with owner-only file permissions, encrypts an empty vault, and verifies decryption access before committing it. Existing identity and vault files are never overwritten. If vault initialization fails after generating an identity, that newly generated file is removed.

For an isolated preview that leaves normal user configuration untouched, put both artifacts under `/private/tmp`:

```sh
bin/tapas init \
  --store /private/tmp/tapas-demo-vault.sops.json \
  --identity /private/tmp/tapas-demo-identity.txt \
  --name demo

bin/tapas serve \
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
bin/tapas serve --store vault.sops.json \
  --name development-api \
  --service example \
  --environment development \
  --env API_TOKEN \
  --reason "Authenticate the development API client"
```

The foreground process opens the default browser and prints one JSON `awaiting_user` event with a fallback loopback URL and expiry. Use `--open=false` to open that URL yourself. The URL contains a short-lived form capability, never a provider credential.

Enter the value in the masked browser field. Save returns a final JSON `saved` event containing the actual reference, such as `store:personal/<id>`, and exits. Cancel, Ctrl-C, SIGTERM, or expiry also stops the server. The default expiry is five minutes; `--ttl 30s` shortens it. A save already in progress is allowed to reach its bounded completion before shutdown, so a committed save is not reported as cancelled.

Validation or encryption errors are shown in the browser without echoing the value. Use Back to correct input; for a revision conflict, cancel and reopen a fresh form. No secret value is accepted through CLI arguments, and there is no command that prints decrypted values.

## Discover and replace

```sh
bin/tapas discover --store vault.sops.json
bin/tapas serve --store vault.sops.json --replace CREDENTIAL_ID
```

Discovery returns the logical store, encrypted-file revision, and credential metadata, without decrypting. Use the exact `id` from discovery for replacement. The form identifies the affected entry and requires a confirmation checkbox. The ID stays stable if the user renames the entry. Duplicate names cannot overwrite an existing entry through the addition flow.

## Storage and lifecycle guarantees

- Names, IDs, descriptions, service/environment labels, suggested variables, and recipient metadata are readable; only values are secret. Do not put secret material in metadata.
- Decryption and re-encryption use memory and private SOPS pipes. The store is decrypted on save, not kept decrypted while the form waits.
- Saves hold a cross-process lock and compare the form's original revision. Concurrent writers using this tool cannot lose updates. Any change since opening the form produces a conflict.
- Updates validate the newly encrypted document by decrypting it in memory, stage only encrypted bytes beside the destination, flush, and atomically replace the file. New files and staging files use owner-only permissions.
- A crash before replacement leaves the original intact; a crash after replacement may have saved successfully even if no final event reached the caller. Check discovery before retrying. A crash may leave an encrypted staging file; never a tool-created plaintext staging file.
- The `.lock` file deliberately remains so concurrent processes share one lock inode. Locks are released by the OS when a process dies. Non-cooperating external editors are outside the locking guarantee.
- The HTTP server binds to `127.0.0.1` on a random port. It validates token, Host, and Origin; rejects cross-origin writes and replay; limits request sizes; disables caching; and serves no external scripts or assets.
- The form has no JavaScript, analytics, browser storage, or request-body logging. Exact runtime-control environment variable names/prefixes are rejected by `Metadata.Validate`; no environment injection is implemented in this slice.

This is not isolation from arbitrary code running as your user. RAM erasure is not guaranteed. Agent session claims, detached request/status persistence, output redaction, and CLI hooks remain future work. No personal vault or identity is created by building or testing the repository.

## Verify

```sh
go test -race ./...
go vet ./...
```

Tests require SOPS and permission to bind loopback ports. They create disposable age identities and synthetic credentials in temporary directories. Coverage includes encrypted round trips, rename/replacement, unchanged originals on failure, duplicate-key rejection, concurrent saves, HTTP token/Host/Origin checks, escaped metadata, replay rejection, cancellation, expiry, and listener shutdown. Browser layout has not yet been manually verified in a graphical browser.

In a restricted development sandbox, point `GOCACHE` and `GOPATH` at writable directories if needed. This does not affect the installed binary.
