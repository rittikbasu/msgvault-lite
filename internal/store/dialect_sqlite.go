package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/mattn/go-sqlite3"
)

// SQLiteDialect implements Dialect for SQLite (the default backend).
type SQLiteDialect struct{}

// Rebind is a no-op for SQLite — it uses ? placeholders natively.
func (d *SQLiteDialect) Rebind(query string) string { return query }

// Now returns the SQLite expression for the current UTC timestamp.
func (d *SQLiteDialect) Now() string { return "datetime('now')" }

// InsertOrIgnore is a no-op for SQLite — the syntax is native.
func (d *SQLiteDialect) InsertOrIgnore(sql string) string { return sql }

// BoolTrueExpr returns "col = 1" — SQLite stores booleans as 0/1 INTEGER.
func (d *SQLiteDialect) BoolTrueExpr(col string) string { return col + " = 1" }

// JSONBindExpr is "?" on SQLite — JSON columns are plain TEXT.
func (d *SQLiteDialect) JSONBindExpr() string { return "?" }

// BuildFTSArg formats search terms as an FTS5 MATCH argument: each
// term double-quote-escaped, suffixed with "*" for prefix match, and
// space-joined (FTS5 treats space as implicit AND). Embedded "*" is
// stripped first so user input cannot break the trailing prefix
// operator. Matches the shape produced by the query package's
// SQLiteQueryDialect.BuildFTSTerm so the API search path and the
// engine deep-search path return the same hits for the same input —
// searching "invo" must match "invoice" in both paths.
//
// Terms that would tokenize to nothing under the default FTS5
// tokenizer (no Unicode letter or digit — e.g. "!!!", "---", "") are
// dropped. If all terms drop, returns "" so the caller can
// short-circuit instead of dispatching a malformed FTS5 MATCH that
// errors at the driver.
func (d *SQLiteDialect) BuildFTSArg(terms []string) string {
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		if !hasFTSToken(t) {
			continue
		}
		t = strings.ReplaceAll(t, `"`, `""`)
		t = strings.ReplaceAll(t, "*", "")
		quoted = append(quoted, `"`+t+`"*`)
	}
	return strings.Join(quoted, " ")
}

// hasFTSToken reports whether s contains at least one rune that the
// default FTS5 tokenizer (unicode61) would emit as part of a token —
// i.e., a Unicode letter or digit. Punctuation-only strings tokenize
// to nothing, so a MATCH built from them is a syntax error.
func hasFTSToken(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// InsertOrIgnorePrefix is a no-op for SQLite — OR IGNORE stays in the prefix.
func (d *SQLiteDialect) InsertOrIgnorePrefix(sql string) string { return sql }

// InsertOrIgnoreSuffix returns "" for SQLite — OR IGNORE is in the statement prefix.
func (d *SQLiteDialect) InsertOrIgnoreSuffix() string { return "" }

// FTSUpsert inserts or replaces an FTS5 row. FTS5 requires rowid to be
// specified explicitly so the virtual table's rowid matches messages.id;
// the dialect owns this detail so callers don't pass messageID twice.
func (d *SQLiteDialect) FTSUpsert(q querier, doc FTSDoc) error {
	_, err := q.Exec(
		`INSERT OR REPLACE INTO messages_fts(rowid, message_id, subject, body, from_addr, to_addr, cc_addr)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		doc.MessageID, doc.MessageID, doc.Subject, doc.Body,
		doc.FromAddr, doc.ToAddrs, doc.CcAddrs,
	)
	return err
}

// FTSSearchClause returns SQL fragments for FTS5 full-text search.
//
// The bm25 weights approximate PostgreSQL's setweight field-priority
// preferences (subject heaviest, then sender, then body / other
// recipients) for typical email shapes. This is a best-effort SQLite
// tuning, NOT a strict cross-backend parity guarantee.
//
// Weights are positional over every column declared in messages_fts —
// UNINDEXED columns count too even though they cannot match — so the
// leading 1.0 is the placeholder for `message_id UNINDEXED`. The
// remaining slots map to (subject, body, from_addr, to_addr, cc_addr).
// PostgreSQL applies setweight 'A'=1.0 to subject and 'B'=0.4 to sender,
// leaving body and other recipients at default 'D'=0.1 — a 10:4:1 ratio,
// which bm25 reproduces as 10/1/4/1/1 across (subject, body, from, to,
// cc). bm25 returns lower (more negative) scores for more relevant rows,
// so callers ORDER BY this expression ascending (the default).
//
// Known divergence: SQLite's bm25() applies Okapi BM25 document-length
// normalization while PostgreSQL's default ts_rank() does not, so very
// long subject-hit documents can still rank below short body-hit
// documents on SQLite while PG ranks them subject-first. See the
// docs-site search ranking page ("Where Ordering Can Diverge") and
// TestFTSRank_KnownDivergence for the expected-behavior pin and
// rationale.
func (d *SQLiteDialect) FTSSearchClause() (join, where, orderBy string, orderArgCount int) {
	return "JOIN messages_fts ON messages_fts.rowid = m.id",
		"messages_fts MATCH ?",
		"bm25(messages_fts, 1.0, 10.0, 1.0, 4.0, 1.0, 1.0)",
		0
}

// FTSDeleteSQL returns the SQL to delete a message's FTS5 entry.
func (d *SQLiteDialect) FTSDeleteSQL() string {
	return `DELETE FROM messages_fts WHERE message_id IN (
		SELECT id FROM messages WHERE source_id = ?
	)`
}

// FTSBackfillBatchSQL returns the SQL to backfill FTS5 for a range of message IDs.
// Parameters: fromID(?), toID(?)
func (d *SQLiteDialect) FTSBackfillBatchSQL() string {
	return `INSERT OR REPLACE INTO messages_fts (rowid, message_id, subject, body, from_addr, to_addr, cc_addr)
		SELECT m.id, m.id, COALESCE(m.subject, ''), COALESCE(mb.body_text, ''),
			COALESCE((SELECT GROUP_CONCAT(p.email_address, ' ') FROM message_recipients mr JOIN participants p ON p.id = mr.participant_id WHERE mr.message_id = m.id AND mr.recipient_type = 'from'), ''),
			COALESCE((SELECT GROUP_CONCAT(p.email_address, ' ') FROM message_recipients mr JOIN participants p ON p.id = mr.participant_id WHERE mr.message_id = m.id AND mr.recipient_type = 'to'), ''),
			COALESCE((SELECT GROUP_CONCAT(p.email_address, ' ') FROM message_recipients mr JOIN participants p ON p.id = mr.participant_id WHERE mr.message_id = m.id AND mr.recipient_type = 'cc'), '')
		FROM messages m
		LEFT JOIN message_bodies mb ON mb.message_id = m.id
		WHERE m.id >= ? AND m.id < ?`
}

// FTSAvailable probes for FTS5 by querying the virtual table.
// Checking sqlite_master alone is insufficient: a binary built without FTS5
// support will fail with "no such module: fts5" even if the table exists.
func (d *SQLiteDialect) FTSAvailable(db *sql.DB) bool {
	var probe int
	err := db.QueryRowContext(context.Background(), "SELECT 1 FROM messages_fts LIMIT 1").Scan(&probe)
	return err == nil || errors.Is(err, sql.ErrNoRows)
}

// FTSNeedsBackfill reports whether the FTS5 table needs population.
// Probes for the existence of ANY message lacking an FTS entry, matching the
// PostgreSQL EXISTS(search_fts IS NULL) semantics. The previous MAX(rowid)
// vs MAX(id) heuristic missed a hole left at a LOW id while later ids were
// indexed — reachable because UpsertFTS failures during sync are
// warn-and-continue (sync.go) while the message row still commits, so id N can
// be unindexed while N+1.. are indexed. messages_fts.rowid == messages.id and
// there are no triggers, so the NOT EXISTS join is rowid-served and cheap on
// FTS5 (no full body scan).
func (d *SQLiteDialect) FTSNeedsBackfill(db *sql.DB) bool {
	var exists bool
	if err := db.QueryRowContext(context.Background(),
		`SELECT EXISTS (
			SELECT 1 FROM messages m
			 WHERE NOT EXISTS (
			     SELECT 1 FROM messages_fts f WHERE f.rowid = m.id
			 )
		)`,
	).Scan(&exists); err != nil {
		return false
	}
	return exists
}

// FTSNeedsBackfillQuick compares MAX(id) against MAX(rowid) — two B-tree
// lookups, instant at any archive size. It catches the dominant staleness
// (tail of the messages table not yet indexed: fresh import, interrupted
// backfill) but misses interior holes; FTSNeedsBackfill stays authoritative.
func (d *SQLiteDialect) FTSNeedsBackfillQuick(db *sql.DB) bool {
	var msgMax int64
	if err := db.QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(id), 0) FROM messages",
	).Scan(&msgMax); err != nil || msgMax == 0 {
		return false
	}
	var ftsMax int64
	if err := db.QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(rowid), 0) FROM messages_fts",
	).Scan(&ftsMax); err != nil {
		return false
	}
	return ftsMax < msgMax
}

// FTSClearSQL returns the SQL to clear all FTS5 data.
func (d *SQLiteDialect) FTSClearSQL() string {
	return "DELETE FROM messages_fts"
}

// SchemaFTS returns the embedded filename containing FTS5 virtual table DDL.
func (d *SQLiteDialect) SchemaFTS() string {
	return "schema_sqlite.sql"
}

// DatabaseSize returns the on-disk size of the SQLite database file.
// Returns (0, nil) for in-memory databases or when the file cannot be stat'd.
func (d *SQLiteDialect) DatabaseSize(_ *sql.DB, dbPath string) (int64, error) {
	if dbPath == "" || dbPath == ":memory:" || strings.Contains(dbPath, ":memory:") {
		return 0, nil
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		return 0, nil //nolint:nilerr // missing/unstattable db file reports 0 size, not an error
	}
	return info.Size(), nil
}

// InitConn is a no-op for SQLite — PRAGMAs are set via DSN parameters.
func (d *SQLiteDialect) InitConn(db *sql.DB) error { return nil }

// SchemaFiles returns the schema files to execute during InitSchema.
func (d *SQLiteDialect) SchemaFiles() []string {
	return []string{"schema.sql"}
}

// CheckpointWAL forces a WAL checkpoint using TRUNCATE mode.
func (d *SQLiteDialect) CheckpointWAL(db *sql.DB) error {
	var busy, log, checkpointed int
	err := db.QueryRowContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	if err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf(
			"WAL checkpoint incomplete: database busy "+
				"(log=%d, checkpointed=%d)", log, checkpointed,
		)
	}
	return nil
}

// SchemaStaleCheck returns the fork-native schema version.
func (d *SQLiteDialect) SchemaStaleCheck() string {
	return "PRAGMA user_version"
}

// IsConflictError returns true if the error is a UNIQUE constraint violation.
func (d *SQLiteDialect) IsConflictError(err error) bool {
	return isSQLiteError(err, "UNIQUE constraint failed")
}

// IsNoSuchTableError returns true if the error indicates a missing table.
func (d *SQLiteDialect) IsNoSuchTableError(err error) bool {
	return isSQLiteError(err, "no such table")
}

// IsNoSuchModuleError returns true if the error indicates a missing module (e.g., fts5).
func (d *SQLiteDialect) IsNoSuchModuleError(err error) bool {
	return isSQLiteError(err, "no such module: fts5")
}

// IsReturningError returns true if the error indicates RETURNING is not supported.
func (d *SQLiteDialect) IsReturningError(err error) bool {
	return isSQLiteError(err, "RETURNING")
}

// BeginExclusive opens a SQLite "BEGIN EXCLUSIVE" transaction on conn.
// In WAL mode this blocks concurrent writers while readers can proceed.
func (d *SQLiteDialect) BeginExclusive(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE")
	return err
}

// BeginWriteSQL returns "BEGIN IMMEDIATE" so the transaction reserves
// the SQLite writer lock at BEGIN, removing the snapshot-isolation race
// that lets two deferred transactions both read the pre-update value.
func (d *SQLiteDialect) BeginWriteSQL() string { return "BEGIN IMMEDIATE" }

// SelectForUpdate returns "" — SQLite has no FOR UPDATE; serialization
// comes from BEGIN IMMEDIATE.
func (d *SQLiteDialect) SelectForUpdate() string { return "" }

// IsBusyError returns true for SQLITE_BUSY and SQLITE_LOCKED. Matching on
// the result code is more robust than substring matching: BUSY surfaces as
// "database is locked" but LOCKED surfaces as "database table is locked",
// so a single substring cannot catch both.
func (d *SQLiteDialect) IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	var serr sqlite3.Error
	if errors.As(err, &serr) {
		return serr.Code == sqlite3.ErrBusy || serr.Code == sqlite3.ErrLocked
	}
	var serrPtr *sqlite3.Error
	if errors.As(err, &serrPtr) && serrPtr != nil {
		return serrPtr.Code == sqlite3.ErrBusy || serrPtr.Code == sqlite3.ErrLocked
	}
	return false
}
