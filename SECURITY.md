# security policy

## reporting a vulnerability

please do not open a public issue for a security vulnerability. use GitHub private vulnerability reporting or contact the maintainer privately with reproduction steps, impact, and any suggested fix.

## threat model

msgvault is a single-user local CLI. it has no server, remote API, multi-user permission model, or privilege separation. anyone who can run commands as the archive owner can already read and modify the same files as msgvault.

The important assets are:

| asset | default location | impact if exposed |
|---|---|---|
| Gmail OAuth token | `~/.msgvault/tokens/` | read access to the authorized Gmail account |
| archive database | `~/.msgvault/msgvault.db` | message metadata, bodies, labels, and search index |
| attachments | `~/.msgvault/attachments/` | archived files and documents |
| config | `~/.msgvault/config.toml` | local paths and possibly sensitive configuration |
| backups | user-selected repository | a copy of archived data and any explicitly included secrets |

msgvault requests read-only Gmail access. it does not trash, delete, send, label, or otherwise mutate remote mail.

## controls

- OAuth tokens and other credential files are written with owner-only permissions
- private directories use owner-only permissions where the platform supports them
- SQLite queries use parameterized statements
- attachment content is addressed by SHA-256 rather than user-controlled path components
- OAuth callback state is validated
- full and incremental sync preserve raw MIME and use resumable checkpoints
- GitHub Actions are pinned to commit SHAs
- `govulncheck` runs in CI

## limitations

### no application-level encryption

The database, attachments, OAuth tokens, and backup repositories are not encrypted by msgvault. filesystem access as the archive owner is enough to read them. use full-disk encryption such as FileVault, BitLocker, or LUKS, and protect backup media separately.

### secrets in backups are opt-in

Backup snapshots exclude config and OAuth tokens by default. including them requires explicit flags. an unencrypted backup repository also requires an additional acknowledgement before plaintext secrets can be captured.

### native dependencies

The current build uses CGO for SQLite and, while the analytics carve is in progress, DuckDB. native dependencies increase supply-chain and memory-safety exposure compared with pure Go. versions are pinned in `go.mod`; dependency updates should be reviewed and tested.

## safe reports

A useful report includes:

- affected command and version
- minimal reproduction
- expected and actual behavior
- whether real credentials or mail data were exposed
- platform and filesystem details relevant to the issue

Do not attach real OAuth tokens, client-secret JSON, message bodies, or private archive files.
