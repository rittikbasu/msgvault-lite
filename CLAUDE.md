# CLAUDE.md

## workflow

Complete requested multi-step work instead of stopping at a plan. Commit every code-producing turn without asking for permission. Before committing, stage the complete intended tree and run the relevant tests, formatting, vet/lint, and build checks.

PR descriptions should be concise and changelog-oriented: what changed, why, and how to use it.

## project

msgvault is a local, read-only Gmail archive for one account. it stores message metadata, raw MIME, parsed bodies, labels, and attachments locally, with SQLite FTS5 search, archive verification, bounded JSON automation, and content-addressed backups.

v0 is deliberately narrow:

- one Gmail account
- Gmail API only
- read-only remote access
- SQLite as the source of truth
- direct CLI; no daemon, HTTP API, MCP, TUI, or remote client
- no generic IMAP, calendar/chat importers, remote deletion, or vector search

Do not reintroduce excluded capabilities or abstractions unless the task explicitly changes product scope.

## command surface

```text
add-account
backup
completion
logs
messages
search
show
status
sync
sync-full
verify
version
```

Write commands create and migrate the database automatically; there is no `init-db` command. `status`, `messages`, `show`, and `search` provide bounded schema-v1 JSON output.

## architecture

```text
cmd/msgvault/                 CLI entrypoint
cmd/msgvault/cmd/             Cobra commands and direct writer locking
internal/backupapp/           snapshot backup, verification, restore proof
internal/config/              local configuration
internal/gmail/               readonly Gmail API client
internal/mime/                MIME parsing
internal/oauth/               Google OAuth flow and token storage
internal/query/               message query engines
internal/search/              Gmail-like query parser
internal/store/               SQLite schema and data access
internal/sync/                full and incremental Gmail synchronization
internal/testutil/            non-assertion test helpers
```

The database is the durable system of record. Gmail deletions become local tombstones; archived content is not deleted. attachments are content-addressed on disk. OAuth tokens and credential files must retain private permissions.

### transition note

The repository is being carved down from a broader upstream project. DuckDB analytics, PostgreSQL branches, and base vector/config code may still exist temporarily while the SQLite-vs-DuckDB benchmark is completed. Treat those as removal candidates, not supported product surface. Do not expand them.

## common commands

```bash
make build
make build-release
make install
make test
make fmt
make lint
make lint-ci
make clean
```

All Go test invocations currently require `-tags "fts5 sqlite_vec"`; prefer `make test` so the project supplies the tags. After the remaining vector residue is removed, update this rule and the Makefile together.

## testing

Use `github.com/stretchr/testify` in all new or modified Go tests.

- `require.X` for setup and fatal preconditions
- `assert.X` for independent checks
- equality order is `(want, got)`: `assert.Equal(t, want, got)`
- never add `t.Errorf`, `t.Fatalf`, `t.Fatal`, or `t.Error`

`internal/testutil` contains non-assertion helpers only. Call testify directly for assertions.

Tests must exercise production behavior, a real parser/validator, or a built artifact. Do not add tautological tests that copy implementation text into a fixture, stub the primary command, and only verify arguments. Do not add tests that grep shell scripts, workflows, or docs for expected source text.

All Go changes must pass:

```bash
make fmt
make test
go vet -tags "fts5 sqlite_vec" ./...
make build
```

Run `make lint-ci` when `golangci-lint` is available.

## data and privacy

Never put real names, email addresses, message content, tokens, client secrets, local home paths, or machine identifiers in fixtures or docs. Use clearly synthetic values such as `alice@example.com`.

Do not read or print secret values during development. It is fine to verify that credential files exist and have correct permissions.

Default local state:

```text
~/.msgvault/config.toml
~/.msgvault/msgvault.db
~/.msgvault/attachments/
~/.msgvault/tokens/
~/.msgvault/logs/
```

`MSGVAULT_HOME` and `--home` override the root.

## database rules

- route database access through `Store`
- use parameterized SQL
- prefer `EXISTS` over `SELECT DISTINCT` plus joins when testing related-row membership
- never scan or join `message_bodies` for list/aggregate/search queries; load bodies by primary key for one message
- use FTS5 for body search; if unavailable, limit fallback search to metadata fields
- preserve resumability, immutable Gmail IDs, local tombstones, and raw MIME retention

## git

Stage all intended changes, including formatting and ancillary files. Inspect `git status` and the staged diff before committing. Commit messages are a single lowercase conventional-commit line with no body or trailers.
