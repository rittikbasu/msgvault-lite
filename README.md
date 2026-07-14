# msgvault

local, read-only Gmail archive with full-text search.

msgvault keeps a durable copy of one Gmail account on your own machine: message metadata, raw MIME, text and HTML bodies, labels, and attachments. it uses SQLite as the source of truth and exposes a small CLI for syncing, searching, verification, automation, and backups.

## why

Gmail is convenient, but it should not be the only copy of years of email. msgvault makes the archive local, inspectable, searchable, and restorable without adding a server or another hosted service.

## what it does

- syncs one Gmail account through the Gmail API
- requests read-only Gmail access and never mutates remote mail
- resumes interrupted full syncs and uses Gmail history for incremental syncs
- preserves raw MIME, parsed bodies, labels, and attachments
- tracks Gmail deletions as local tombstones instead of deleting archived data
- provides SQLite FTS5 search with Gmail-like filters
- offers bounded, versioned JSON output for scripts and agents
- verifies SQLite integrity, Gmail parity, and sampled raw MIME
- creates content-addressed backup snapshots and proves restores

## build

msgvault currently builds from source and requires Go 1.26.5 plus a working C toolchain.

from a repository clone:

```bash
make build

# optional: install to ~/.local/bin or GOPATH/bin
make install
```

## set up Gmail access

create a desktop OAuth client in Google Cloud, enable the Gmail API, download the client secrets JSON, and point msgvault at it:

```toml
# ~/.msgvault/config.toml
[oauth]
client_secrets = "/absolute/path/to/client_secret.json"
```

then authorize the account:

```bash
msgvault add-account you@gmail.com

# use this when the archive machine has no browser
msgvault add-account you@gmail.com --headless
```

adding the account creates and migrates the local database automatically. v0 intentionally supports one Gmail account.

## sync

```bash
# initial or resumable full sync
msgvault sync-full you@gmail.com

# useful for a small first run
msgvault sync-full you@gmail.com --limit 100

# optional date range or Gmail query
msgvault sync-full you@gmail.com --after 2024-01-01
msgvault sync-full you@gmail.com --query 'from:someone@example.com'

# incremental sync after the full sync establishes a history cursor
msgvault sync you@gmail.com
```

## browse and search

```bash
msgvault status
msgvault messages --limit 20
msgvault show 12345

msgvault search 'project update'
msgvault search 'from:alice@example.com has:attachment'
msgvault search 'subject:invoice newer_than:1y'
```

search supports bare full-text terms plus `from:`, `to:`, `cc:`, `bcc:`, `subject:`, `label:`, `has:attachment`, `before:`, `after:`, `older_than:`, `newer_than:`, `larger:`, and `smaller:`.

`status`, `messages`, `show`, and `search` support `--json`. list/search pages are capped at 200 rows, and `show --json` caps each body field by default so automation cannot accidentally dump an unbounded archive.

## verify the archive

```bash
msgvault verify you@gmail.com
msgvault verify you@gmail.com --sample 200 --json
```

verification runs SQLite integrity checks, compares exact local and Gmail message ID sets (including spam and trash), checks raw MIME coverage, and decompresses a random sample. because Gmail can change during enumeration, retry after syncing if verification reports a transient remote mismatch.

## backups

```bash
msgvault backup init --repo /path/to/backup
msgvault backup create --repo /path/to/backup
msgvault backup list --repo /path/to/backup
msgvault backup verify --repo /path/to/backup

# restore the latest snapshot into a separate directory and prove it
msgvault backup restore --repo /path/to/backup --target /tmp/msgvault-restore
```

config and OAuth tokens are excluded by default. including them requires explicit flags; plaintext repositories also require an additional acknowledgement.

## storage

by default, msgvault keeps its state under `~/.msgvault/`:

- `msgvault.db` — SQLite source of truth
- `attachments/` — attachment content addressed by hash
- `tokens/` — OAuth tokens with private file permissions
- `logs/` — structured local logs

set `MSGVAULT_HOME` or pass `--home` to use a different directory.

## scope

v0 is deliberately narrow: one Gmail account, local SQLite, direct CLI, and read-only remote access. it does not ship a daemon, web API, TUI, generic IMAP, other chat/mail importers, remote deletion, or vector search.

## development

```bash
make test
make lint
make build
```

## credits

msgvault started as a focused fork of [kenn-io/msgvault](https://github.com/kenn-io/msgvault). the upstream architecture and much of the durable Gmail archive core were built by Wes McKinney and contributors.

## license

MIT
