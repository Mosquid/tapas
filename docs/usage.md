# Advanced usage and security details

Start with the [README](../README.md) for installation and a first credential.

## Configuration and storage

The installer accepts `--version <tag>` to pin a release, `--install-dir <path>`
to choose a binary directory, and `--no-skill` to skip agent skill installation.
Pass options after `sh -s --`, for example:

```sh
curl -fsSL https://raw.githubusercontent.com/Mosquid/tapas/main/install.sh | \
  sh -s -- --install-dir "$HOME/bin" --no-skill
```

By default, the skill is installed to `~/.claude/skills/agent-secrets`. If `codex`
is on `PATH`, it is also installed to `~/.agents/skills/agent-secrets`. Override
these locations with `--skill-dir <path>` and `--codex-skill-dir <path>`;
the latter explicitly requests Codex skill installation even if it isn't detected.

The default vault is `tapas/vault.sops.json` inside your OS user configuration
directory. The private key is `tapas/age-identity.txt` in the same directory.
Initialization creates the key automatically and never overwrites an existing
key or vault.

Use `--store <path>` for a separate vault and `--identity <path>` for a different
private key. Pass the same paths to subsequent commands. To initialize with an
existing key, supply both its public recipient with `--age` and its private file
with `--identity`.

When syncing an encrypted vault, keep its private key separate. Never commit the
key. The current format supports a strict JSON schema and one age recipient;
YAML, KMS, multiple recipients, and arbitrary existing SOPS files are unsupported.
A `.sops.yaml` file is not required.

## Browser forms and credential management

`add`, `edit`, and `delete` run a temporary local server and open your browser.
Use `--open=false` to open the printed URL yourself, or `--ttl 30s` to shorten
the default five-minute expiry. Save, Cancel, interruption, and expiry stop the
server. The URL contains a short-lived form capability; treat it as private.

`edit` changes metadata without changing the secret. `add --replace <id>` replaces
a value after browser confirmation. Both preserve the credential's ID and
reference. Duplicate names cannot overwrite an existing entry through addition.
`delete` permanently removes an entry after browser confirmation; `remove` is an alias.

If another process changes the vault while a form is open, the save fails with a
revision conflict. Cancel and reopen the form to work from the latest version.
After an interrupted save, check `tapas list` before retrying: the save may have
completed even if its final event wasn't printed.

## Limited previews

Use metadata to identify a credential whenever possible. When you need to check
its shape, `preview` returns metadata, character count, and a masked fragment:

```sh
tapas preview --ref store:personal/CREDENTIAL_ID
```

It reveals at most one quarter of the characters, capped at eight, prioritizing
the first four and then the end. Values shorter than four characters are fully
masked. The fragment becomes visible in tool output, so treat it as disclosed
secret material. A plausible prefix or length does not prove validity.

## Execution and security boundaries

`run` resolves credentials by immutable ID, decrypts in memory, and returns the
child command's exit status. Bare `--ref REF` uses the stored suggested variable;
`--ref VARIABLE=REF` overrides it. Multiple credentials cannot target the same
variable. Runtime-control environment variables are rejected.

The runner redacts exact values from captured stdout and stderr, including values
split across writes. It holds back a short output tail to do this, so it is suited
to non-interactive commands. It cannot redact encoded or transformed values, or
output written directly to a terminal, file, or network destination. Child code
can access the supplied credentials and pass them to its own subprocesses.

Names, IDs, descriptions, service/environment labels, suggested variables, and
recipient metadata are readable. Never put secret material in these fields.
There is no command that prints a full decrypted value.

Vault updates use a cross-process lock and revision checks. They validate the
new encrypted document, stage only encrypted bytes beside the destination, and
atomically replace the file. New vault, key, and staging files use owner-only
permissions. A crash may leave an encrypted staging file. The `.lock` file stays
in place so processes share the same lock; the OS releases locks when a process
dies. External editors do not participate in this protection.

The browser server binds to `127.0.0.1` on a random port, validates its token,
Host, and Origin, rejects replay, limits request sizes, and disables caching.
Decryption and re-encryption use memory and private SOPS pipes; the vault is not
kept decrypted while the form waits.

Tapas does not protect against arbitrary code running as your user, and memory
erasure is not guaranteed. Credentials are delivered through the explicit
`tapas run` path; transparent agent hooks and delivery into an already running
process are not implemented.
