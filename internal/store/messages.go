package store

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/mime"
)

// querier is satisfied by both *sql.DB and *sql.Tx, allowing
// helpers to run inside or outside a transaction.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// RecipientSet groups participant IDs and display names for one
// recipient type (from, to, cc, bcc).
type RecipientSet struct {
	Type           string
	ParticipantIDs []int64
	DisplayNames   []string
}

// MessagePersistData bundles everything needed to atomically
// persist a message and its related rows in a single transaction.
type MessagePersistData struct {
	Message    *Message
	BodyText   sql.NullString
	BodyHTML   sql.NullString
	RawMIME    []byte
	Recipients []RecipientSet
	LabelIDs   []int64
}

// Message represents a message in the database.
type Message struct {
	ID              int64
	ConversationID  int64
	SourceID        int64
	SourceMessageID string
	SentAt          sql.NullTime
	InternalDate    sql.NullTime
	SenderID        sql.NullInt64
	Subject         sql.NullString
	Snippet         sql.NullString
	SizeEstimate    int64
	HasAttachments  bool
	AttachmentCount int
	DeletedAt       sql.NullTime
	ArchivedAt      time.Time
}

// ErrMessagesInsertOnly reports an attempted physical deletion from the archive.
var ErrMessagesInsertOnly = errors.New("message rows are insert-only")

// MessageExistsBatch checks which message IDs already exist in the database.
// Returns a map of source_message_id -> internal message_id for existing messages.
func (s *Store) MessageExistsBatch(sourceID int64, sourceMessageIDs []string) (map[string]int64, error) {
	if len(sourceMessageIDs) == 0 {
		return make(map[string]int64), nil
	}

	result := make(map[string]int64)
	err := queryInChunks(s.db, sourceMessageIDs, []any{sourceID},
		`SELECT source_message_id, id FROM messages WHERE source_id = ? AND source_message_id IN (%s)`,
		func(rows *loggedRows) error {
			var srcID string
			var id int64
			if err := rows.Scan(&srcID, &id); err != nil {
				return err
			}
			result[srcID] = id
			return nil
		})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// MessageExistsWithRawBatch checks which messages have retained raw MIME.
func (s *Store) MessageExistsWithRawBatch(sourceID int64, sourceMessageIDs []string) (map[string]int64, error) {
	if len(sourceMessageIDs) == 0 {
		return make(map[string]int64), nil
	}

	result := make(map[string]int64)
	err := queryInChunks(s.db, sourceMessageIDs, []any{sourceID},
		`SELECT m.source_message_id, m.id
		 FROM messages m
		 JOIN message_raw mr ON mr.message_id = m.id
		 WHERE m.source_id = ? AND m.source_message_id IN (%s)`,
		func(rows *loggedRows) error {
			var srcID string
			var id int64
			if err := rows.Scan(&srcID, &id); err != nil {
				return err
			}
			result[srcID] = id
			return nil
		})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// EnsureConversation gets or creates a conversation (thread) for a message.
// Concurrent first-inserts converge via INSERT ... ON CONFLICT DO UPDATE
// RETURNING id: the no-op SET fires the RETURNING clause for both the
// insert and the conflict path, so the second caller still receives the
// existing row's id instead of a unique-violation error.
func (s *Store) EnsureConversation(sourceID int64, sourceConversationID, title string) (int64, error) {
	now := s.dialect.Now()
	var id int64
	err := s.db.QueryRow(fmt.Sprintf(`
		INSERT INTO conversations (source_id, source_conversation_id, title, created_at, updated_at)
		VALUES (?, ?, ?, %s, %s)
		ON CONFLICT (source_id, source_conversation_id) DO UPDATE
		SET source_conversation_id = conversations.source_conversation_id
		RETURNING id
	`, now, now), sourceID, sourceConversationID, title).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// upsertMessageSQL returns the message upsert SQL with dialect-specific ID and timestamp expressions.
func upsertMessageSQL(id, now string) string {
	return fmt.Sprintf(`
	INSERT INTO messages (
		id,
		conversation_id, source_id, source_message_id,
		sent_at, internal_date, sender_id,
		subject, snippet, size_estimate,
		has_attachments, attachment_count, archived_at
	) VALUES (%s, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, %s)
	ON CONFLICT(source_id, source_message_id) DO UPDATE SET
		conversation_id = excluded.conversation_id,
		sent_at = excluded.sent_at,
		internal_date = excluded.internal_date,
		sender_id = excluded.sender_id,
		subject = excluded.subject,
		snippet = excluded.snippet,
		size_estimate = excluded.size_estimate,
		has_attachments = excluded.has_attachments,
		attachment_count = excluded.attachment_count`, id, now)
}

// nextMessageIDSQL allocates a monotonic permanent archive ID. Unix milliseconds
// remain exactly representable by JavaScript numbers while MAX(id)+1 preserves
// ordering when several messages are persisted in the same second.
func nextMessageIDSQL() string {
	return `(SELECT MAX(
		COALESCE(MAX(id) + 1, 1),
		CAST(strftime('%s', 'now') AS INTEGER) * 1000
	) FROM messages)`
}

// UpsertMessage inserts or updates a message.
func (s *Store) UpsertMessage(msg *Message) (int64, error) {
	return upsertMessageWith(s.db, s.dialect, msg)
}

func upsertMessageWith(q querier, d Dialect, msg *Message) (int64, error) {
	sql := upsertMessageSQL(nextMessageIDSQL(), d.Now())
	args := []any{
		msg.ConversationID, msg.SourceID, msg.SourceMessageID,
		msg.SentAt, msg.InternalDate, msg.SenderID,
		msg.Subject, msg.Snippet, msg.SizeEstimate,
		msg.HasAttachments, msg.AttachmentCount,
	}

	// Use RETURNING to avoid an extra SELECT per message when supported.
	var id int64
	err := q.QueryRow(sql+"\n\t\tRETURNING id\n\t", args...).Scan(&id)

	if err != nil {
		// SQLite < 3.35 does not support RETURNING. Fall back to an Exec + SELECT.
		if !d.IsReturningError(err) {
			return 0, err
		}

		if _, execErr := q.Exec(sql, args...); execErr != nil {
			return 0, execErr
		}

		if err := q.QueryRow(
			`SELECT id FROM messages WHERE source_id = ? AND source_message_id = ?`,
			msg.SourceID, msg.SourceMessageID,
		).Scan(&id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// UpsertMessageBody stores the body text and HTML for a message in the separate message_bodies table.
func (s *Store) UpsertMessageBody(messageID int64, bodyText, bodyHTML sql.NullString) error {
	return upsertMessageBody(s.db, messageID, bodyText, bodyHTML)
}

func upsertMessageBody(q querier, messageID int64, bodyText, bodyHTML sql.NullString) error {
	_, err := q.Exec(`
		INSERT INTO message_bodies (message_id, body_text, body_html)
		VALUES (?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET
			body_text = excluded.body_text,
			body_html = excluded.body_html
	`, messageID, bodyText, bodyHTML)
	return err
}

// UpsertMessageRaw stores the compressed raw MIME data for a message.
func (s *Store) UpsertMessageRaw(messageID int64, rawData []byte) error {
	return upsertMessageRaw(s.db, messageID, rawData)
}

func upsertMessageRaw(q querier, messageID int64, rawData []byte) error {
	// Compress with zlib
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if _, err := w.Write(rawData); err != nil {
		return fmt.Errorf("compress: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close compressor: %w", err)
	}

	_, err := q.Exec(`
		INSERT INTO message_raw (message_id, raw_data, raw_format, compression)
		VALUES (?, ?, 'mime', 'zlib')
		ON CONFLICT(message_id) DO UPDATE SET
			raw_data = excluded.raw_data,
			raw_format = excluded.raw_format,
			compression = excluded.compression
	`, messageID, compressed.Bytes())
	return err
}

// GetMessageRaw retrieves and decompresses the raw MIME data for a message.
func (s *Store) GetMessageRaw(messageID int64) ([]byte, error) {
	var compressed []byte
	var compression sql.NullString

	err := s.db.QueryRow(`
		SELECT raw_data, compression FROM message_raw WHERE message_id = ?
	`, messageID).Scan(&compressed, &compression)
	if err != nil {
		return nil, err
	}

	if compression.Valid && compression.String == "zlib" {
		r, err := zlib.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, fmt.Errorf("zlib reader: %w", err)
		}
		defer func() { _ = r.Close() }()
		return io.ReadAll(r)
	}

	return compressed, nil
}

// PersistMessage atomically stores a message plus its body, raw MIME,
// recipients, and labels in a single transaction. Returns the message ID.
func (s *Store) PersistMessage(data *MessagePersistData) (int64, error) {
	var messageID int64
	err := s.withTx(func(tx *loggedTx) error {
		id, err := upsertMessageWith(tx, s.dialect, data.Message)
		if err != nil {
			return fmt.Errorf("upsert message: %w", err)
		}
		messageID = id

		if err := upsertMessageBody(tx, messageID, data.BodyText, data.BodyHTML); err != nil {
			return fmt.Errorf("upsert body: %w", err)
		}

		if len(data.RawMIME) > 0 {
			if err := upsertMessageRaw(tx, messageID, data.RawMIME); err != nil {
				return fmt.Errorf("store raw: %w", err)
			}
		}

		for _, rs := range data.Recipients {
			if err := replaceMessageRecipientsTx(tx, messageID, rs); err != nil {
				return fmt.Errorf("store %s recipients: %w", rs.Type, err)
			}
		}

		if err := replaceMessageLabelsTx(tx, messageID, data.LabelIDs); err != nil {
			return fmt.Errorf("store labels: %w", err)
		}

		return nil
	})
	return messageID, err
}

// Participant represents a person in the participants table.
type Participant struct {
	ID           int64
	EmailAddress sql.NullString
	DisplayName  sql.NullString
	Domain       sql.NullString
}

// EnsureParticipant gets or creates a participant by email. Atomic via
// INSERT … ON CONFLICT … RETURNING id so two goroutines (or two
// processes against PostgreSQL) cannot race between a SELECT-empty and
// the follow-up INSERT and both succeed — one would otherwise lose to
// the unique constraint on (email_address) with a 23505 error. Display
// name and domain are left untouched on conflict to preserve any
// hand-edited values.
func (s *Store) EnsureParticipant(email, displayName, domain string) (int64, error) {
	// ON CONFLICT must mirror the partial unique index on
	// participants(email_address) WHERE email_address IS NOT NULL — both
	// PG and SQLite require the WHERE clause on the conflict target to
	// match the partial index exactly. DO UPDATE (no-op assignment on
	// the same column) makes RETURNING fire for both INSERT and the
	// existing-row case, giving us the id either way.
	var id int64
	err := s.db.QueryRow(fmt.Sprintf(`
		INSERT INTO participants (email_address, display_name, domain, created_at, updated_at)
		VALUES (?, ?, ?, %s, %s)
		ON CONFLICT (email_address) WHERE email_address IS NOT NULL
			DO UPDATE SET email_address = EXCLUDED.email_address
		RETURNING id
	`, s.dialect.Now(), s.dialect.Now()), email, displayName, domain).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// EnsureParticipantsBatch gets or creates participants in batch.
// Returns a map of email -> participant ID.
func (s *Store) EnsureParticipantsBatch(addresses []mime.Address) (map[string]int64, error) {
	if len(addresses) == 0 {
		return make(map[string]int64), nil
	}

	result := make(map[string]int64)

	// First, try to insert all (ignoring conflicts)
	insertSQL := s.dialect.InsertOrIgnore(fmt.Sprintf(`INSERT OR IGNORE INTO participants (email_address, display_name, domain, created_at, updated_at)
			VALUES (?, ?, ?, %s, %s)`, s.dialect.Now(), s.dialect.Now()))
	for _, addr := range addresses {
		if addr.Email == "" {
			continue
		}
		if _, err := s.db.Exec(insertSQL, addr.Email, addr.Name, addr.Domain); err != nil {
			return nil, err
		}
	}

	// Then fetch all IDs
	emails := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		if addr.Email != "" {
			emails = append(emails, addr.Email)
		}
	}

	if len(emails) == 0 {
		return result, nil
	}

	err := queryInChunks(s.db, emails, nil,
		`SELECT email_address, id FROM participants WHERE email_address IN (%s)`,
		func(rows *loggedRows) error {
			var email string
			var id int64
			if err := rows.Scan(&email, &id); err != nil {
				return err
			}
			result[email] = id
			return nil
		})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReplaceMessageRecipients replaces all recipients for a message atomically.
func (s *Store) ReplaceMessageRecipients(messageID int64, recipientType string, participantIDs []int64, displayNames []string) error {
	return s.withTx(func(tx *loggedTx) error {
		return replaceMessageRecipientsTx(tx, messageID, RecipientSet{
			Type:           recipientType,
			ParticipantIDs: participantIDs,
			DisplayNames:   displayNames,
		})
	})
}

func replaceMessageRecipientsTx(tx *loggedTx, messageID int64, rs RecipientSet) error {
	_, err := tx.Exec(`
		DELETE FROM message_recipients WHERE message_id = ? AND recipient_type = ?
	`, messageID, rs.Type)
	if err != nil {
		return err
	}

	if len(rs.ParticipantIDs) == 0 {
		return nil
	}

	// Collapse duplicate participants within this set. The table holds at most
	// one row per (message_id, participant_id, recipient_type), so a participant
	// repeated in one call — a calendar event listing the same attendee twice, or
	// two address forms that resolve to the same participant — is redundant and
	// would otherwise trip the UNIQUE constraint and abort the entire write. The
	// first occurrence's display name wins.
	seen := make(map[int64]struct{}, len(rs.ParticipantIDs))
	ids := make([]int64, 0, len(rs.ParticipantIDs))
	names := make([]string, 0, len(rs.ParticipantIDs))
	for i, pid := range rs.ParticipantIDs {
		if _, dup := seen[pid]; dup {
			continue
		}
		seen[pid] = struct{}{}
		ids = append(ids, pid)
		name := ""
		if i < len(rs.DisplayNames) {
			name = rs.DisplayNames[i]
		}
		names = append(names, name)
	}

	return insertInChunks(tx, chunkInsert{
		totalRows:    len(ids),
		valuesPerRow: 4,
		prefix:       "INSERT INTO message_recipients (message_id, participant_id, recipient_type, display_name) VALUES ",
	}, func(start, end int) ([]string, []any) {
		values := make([]string, end-start)
		args := make([]any, 0, (end-start)*4)
		for i := start; i < end; i++ {
			values[i-start] = "(?, ?, ?, ?)"
			args = append(args, messageID, ids[i], rs.Type, names[i])
		}
		return values, args
	})
}

// Label represents a Gmail label.
type Label struct {
	ID            int64
	SourceID      sql.NullInt64
	SourceLabelID sql.NullString
	Name          string
	LabelType     sql.NullString
}

// EnsureLabel gets or creates a label, handling renames and ID changes.
// For batch operations prefer EnsureLabelsBatch which runs in a single
// transaction.
func (s *Store) EnsureLabel(
	sourceID int64,
	sourceLabelID, name, labelType string,
) (int64, error) {
	var id int64
	err := s.withTx(func(tx *loggedTx) error {
		var txErr error
		id, txErr = ensureLabelWith(
			tx, sourceID, sourceLabelID, name, labelType,
		)
		return txErr
	})
	return id, err
}

// ensureLabelWith is the core label-upsert logic, parameterised on the
// database handle so it works both standalone and inside a transaction.
// The handle is expected to be *loggedDB or *loggedTx so placeholder
// rebinding is applied automatically.
//
// Labels are identified by source_label_id (Gmail label ID) but have a
// UNIQUE constraint on (source_id, name). This function handles:
//   - Existing label found by source_label_id: updates name if renamed
//   - Name conflict with different source_label_id: upserts, adopting
//     the new source_label_id (handles deleted+recreated labels, imports)
func ensureLabelWith(
	q querier,
	sourceID int64,
	sourceLabelID, name, labelType string,
) (int64, error) {
	// Look up by canonical identifier (Gmail label ID).
	var id int64
	var existingName string
	var existingType sql.NullString
	err := q.QueryRow(`
		SELECT id, name, label_type FROM labels
		WHERE source_id = ? AND source_label_id = ?
	`, sourceID, sourceLabelID).Scan(&id, &existingName, &existingType)

	if err == nil {
		if existingName == name {
			if !existingType.Valid || existingType.String != labelType {
				if _, err = q.Exec(`
					UPDATE labels SET label_type = ?
					WHERE id = ?
				`, labelType, id); err != nil {
					return 0, fmt.Errorf("update label type: %w", err)
				}
			}
			return id, nil
		}
		// Label was renamed — update the name. If another row already
		// claims the target name, merge it: move its message-label
		// associations to the canonical row and delete the stale one.
		if err = mergeLabelByName(q, sourceID, name, id); err != nil {
			return 0, err
		}
		if _, err = q.Exec(`
			UPDATE labels SET name = ?, label_type = ?
			WHERE id = ?
		`, name, labelType, id); err != nil {
			return 0, fmt.Errorf("update label name: %w", err)
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	// Not found by source_label_id — upsert by name. Handles the case
	// where a label with this name exists from a previous import or
	// with a stale/NULL source_label_id.
	if _, err = q.Exec(`
		INSERT INTO labels (source_id, source_label_id, name, label_type)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_id, name) DO UPDATE SET
			source_label_id = excluded.source_label_id,
			label_type = excluded.label_type
	`, sourceID, sourceLabelID, name, labelType); err != nil {
		return 0, err
	}

	err = q.QueryRow(`
		SELECT id FROM labels WHERE source_id = ? AND name = ?
	`, sourceID, name).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// mergeLabelByName finds a label with the given name (excluding keepID)
// and merges it into keepID: message-label associations are reassigned
// and the stale row is deleted. No-op if no conflicting label exists.
func mergeLabelByName(
	q querier, sourceID int64, name string, keepID int64,
) error {
	var conflictID int64
	err := q.QueryRow(`
		SELECT id FROM labels
		WHERE source_id = ? AND name = ? AND id != ?
	`, sourceID, name, keepID).Scan(&conflictID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find conflicting label: %w", err)
	}
	// Drop associations that would conflict after reassignment (message
	// already linked to keepID). This is the portable equivalent of
	// SQLite's UPDATE OR IGNORE — done explicitly so PostgreSQL works the
	// same way.
	if _, err = q.Exec(`
		DELETE FROM message_labels
		WHERE label_id = ?
		AND message_id IN (
			SELECT message_id FROM message_labels WHERE label_id = ?
		)
	`, conflictID, keepID); err != nil {
		return fmt.Errorf("drop conflicting associations: %w", err)
	}
	// Reassign the remaining associations (no PK violations possible now).
	if _, err = q.Exec(`
		UPDATE message_labels SET label_id = ? WHERE label_id = ?
	`, keepID, conflictID); err != nil {
		return fmt.Errorf("reassign label associations: %w", err)
	}
	if _, err = q.Exec(`
		DELETE FROM labels WHERE id = ?
	`, conflictID); err != nil {
		return fmt.Errorf("delete conflicting label: %w", err)
	}
	return nil
}

// LabelInfo holds the name and type for a label to be ensured.
type LabelInfo struct {
	Name string
	Type string // "system" or "user"
}

// IsSystemLabel returns true if the given Gmail label ID represents a system label.
func IsSystemLabel(sourceLabelID string) bool {
	switch sourceLabelID {
	case "INBOX", "SENT", "TRASH", "SPAM", "DRAFT", "UNREAD", "STARRED", "IMPORTANT":
		return true
	}
	return strings.HasPrefix(sourceLabelID, "CATEGORY_")
}

// EnsureLabelsBatch ensures all labels exist and returns a map of
// source_label_id -> internal ID. Runs in a single transaction with
// a two-phase rename to handle cross-renames safely (e.g. L1:Foo→Bar
// and L2:Bar→Foo in the same batch).
func (s *Store) EnsureLabelsBatch(
	sourceID int64, labels map[string]LabelInfo,
) (map[string]int64, error) {
	result := make(map[string]int64, len(labels))
	err := s.withTx(func(tx *loggedTx) error {
		// Phase 1: Move all renamed labels to temporary names so
		// that cross-renames don't cause one label to incorrectly
		// merge the other. Temp names embed the row PK (unique by
		// construction within this source_id) and a SOH (U+0001)
		// prefix that real Gmail label names cannot contain — Gmail's
		// UI rejects control characters, so the temp name cannot
		// collide with any real label name in the same source. The
		// SQLite-only X'00' hex literal that previously played this
		// role is not portable: PostgreSQL doesn't parse X'00' and
		// PG TEXT rejects embedded NUL bytes outright, so we build
		// the sentinel in Go and bind it as a parameter.
		for sourceLabelID, info := range labels {
			var id int64
			var curName string
			err := tx.QueryRow(`
				SELECT id, name FROM labels
				WHERE source_id = ? AND source_label_id = ?
			`, sourceID, sourceLabelID).Scan(&id, &curName)
			if errors.Is(err, sql.ErrNoRows) || curName == info.Name {
				continue
			}
			if err != nil {
				return fmt.Errorf(
					"check label %s: %w", sourceLabelID, err,
				)
			}
			tempName := fmt.Sprintf("\x01__msgvault_pending_rename__%d", id)
			if _, err = tx.Exec(`
				UPDATE labels SET name = ? WHERE id = ?
			`, tempName, id); err != nil {
				return fmt.Errorf(
					"clear name for label %s: %w", sourceLabelID, err,
				)
			}
		}

		// Phase 2: Apply final names. After phase 1 any remaining
		// name conflict is from a label NOT in this batch, which
		// is safe to merge (dead/imported label).
		for sourceLabelID, info := range labels {
			id, err := ensureLabelWith(
				tx, sourceID, sourceLabelID, info.Name, info.Type,
			)
			if err != nil {
				return err
			}
			result[sourceLabelID] = id
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReplaceMessageLabels replaces all labels for a message atomically.
func (s *Store) ReplaceMessageLabels(messageID int64, labelIDs []int64) error {
	return s.withTx(func(tx *loggedTx) error {
		return replaceMessageLabelsTx(tx, messageID, labelIDs)
	})
}

func replaceMessageLabelsTx(tx *loggedTx, messageID int64, labelIDs []int64) error {
	_, err := tx.Exec(`
		DELETE FROM message_labels WHERE message_id = ?
	`, messageID)
	if err != nil {
		return err
	}

	if len(labelIDs) == 0 {
		return nil
	}

	return insertInChunks(tx, chunkInsert{
		totalRows:    len(labelIDs),
		valuesPerRow: 2,
		prefix:       "INSERT INTO message_labels (message_id, label_id) VALUES ",
	}, func(start, end int) ([]string, []any) {
		values := make([]string, end-start)
		args := make([]any, 0, (end-start)*2)
		for i := start; i < end; i++ {
			values[i-start] = "(?, ?)"
			args = append(args, messageID, labelIDs[i])
		}
		return values, args
	})
}

// AddMessageLabels adds labels to a message without removing existing ones.
// Uses INSERT OR IGNORE to skip labels that already exist.
func (s *Store) AddMessageLabels(messageID int64, labelIDs []int64) error {
	if len(labelIDs) == 0 {
		return nil
	}
	return s.withTx(func(tx *loggedTx) error {
		return insertInChunks(tx, chunkInsert{
			totalRows:    len(labelIDs),
			valuesPerRow: 2,
			prefix:       s.dialect.InsertOrIgnorePrefix("INSERT OR IGNORE INTO message_labels (message_id, label_id) VALUES "),
			suffix:       s.dialect.InsertOrIgnoreSuffix(),
		}, func(start, end int) ([]string, []any) {
			values := make([]string, end-start)
			args := make([]any, 0, (end-start)*2)
			for i := start; i < end; i++ {
				values[i-start] = "(?, ?)"
				args = append(args, messageID, labelIDs[i])
			}
			return values, args
		})
	})
}

// LinkMessageLabel links a single label to a message.
// Uses INSERT OR IGNORE — safe to call multiple times.
func (s *Store) LinkMessageLabel(messageID, labelID int64) error {
	return s.AddMessageLabels(messageID, []int64{labelID})
}

// RemoveMessageLabels removes specific labels from a message.
func (s *Store) RemoveMessageLabels(messageID int64, labelIDs []int64) error {
	if len(labelIDs) == 0 {
		return nil
	}
	return execInChunks(s.db, labelIDs, []any{messageID},
		`DELETE FROM message_labels WHERE message_id = ? AND label_id IN (%s)`)
}

// SetReplyTo links a channel reply to its parent by resolving the parent's
// source_message_id to its internal messages.id within the same source.
func (s *Store) MarkMessageDeleted(sourceID int64, sourceMessageID string) error {
	_, err := s.db.Exec(fmt.Sprintf(`
		UPDATE messages
		SET deleted_from_source_at = %s
		WHERE source_id = ? AND source_message_id = ?
	`, s.dialect.Now()), sourceID, sourceMessageID)
	return err
}

// MarkMessagesDeletedBatch marks multiple messages as deleted from the source in a single transaction.
func (s *Store) MarkMessagesDeletedBatch(sourceID int64, sourceMessageIDs []string) error {
	if len(sourceMessageIDs) == 0 {
		return nil
	}
	return execInChunks(s.db, sourceMessageIDs, []any{sourceID},
		fmt.Sprintf(`UPDATE messages SET deleted_from_source_at = %s WHERE source_id = ? AND source_message_id IN (%%s)`, s.dialect.Now()))
}

// MarkMessageDeletedByGmailID marks a message as deleted by its Gmail ID.
// The permanent argument describes the source-side operation only; archive rows
// are always retained as tombstones.
//
// A2 (deferred): the match is NOT scoped by source_id, so a Gmail-ID collision
// across two accounts would tombstone the wrong account's row (blast radius:
// one row in the colliding account). This is deferred rather than fixed
// because the deletion Manifest carries only a flat []GmailIDs with no per-id
// source_id (internal/deletion/manifest.go), and a single manifest can legitimately
// span multiple accounts (see internal/tui/actions.go resolveGmailIDs /
// internal/mcp/handlers.go, where the account filter is optional), so a single
// Filters.Account cannot scope every id correctly. Properly scoping this needs a
// manifest schema/version change (out of scope). Gmail IDs are random enough that
// a cross-account collision is astronomically unlikely. See docs/internal/PG_STATUS.md.
func (s *Store) MarkMessageDeletedByGmailID(_ bool, gmailID string) error {
	// A2 (deferred): unscoped by source_id — see function doc.
	_, err := s.db.Exec(fmt.Sprintf(`
		UPDATE messages
		SET deleted_from_source_at = %s
		WHERE source_message_id = ?
	`, s.dialect.Now()), gmailID)
	return err
}

// MarkMessagesDeletedByGmailIDBatch marks multiple messages as deleted by their Gmail IDs
// in batched UPDATE statements. Much faster than individual MarkMessageDeletedByGmailID calls
// because it issues one UPDATE per chunk instead of one per message.
//
// Uses best-effort semantics: if a chunk fails, it falls back to individual updates
// for that chunk and continues with remaining chunks. Returns the first error encountered
// (if any) after processing all IDs.
//
// A2 (deferred): the IN (...) match is NOT scoped by source_id — same unscoped
// collision caveat as MarkMessageDeletedByGmailID; see that function's doc and
// docs/internal/PG_STATUS.md for why it is deferred (manifest lacks per-id source_id and
// can span multiple accounts; collision astronomically unlikely).
func (s *Store) MarkMessagesDeletedByGmailIDBatch(gmailIDs []string) error {
	if len(gmailIDs) == 0 {
		return nil
	}

	const chunkSize = 500
	var firstErr error

	for i := 0; i < len(gmailIDs); i += chunkSize {
		end := min(i+chunkSize, len(gmailIDs))
		chunk := gmailIDs[i:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for j, id := range chunk {
			placeholders[j] = "?"
			args[j] = id
		}

		// A2 (deferred): unscoped by source_id — see function doc.
		query := fmt.Sprintf(
			`UPDATE messages SET deleted_from_source_at = %s WHERE source_message_id IN (%s)`,
			s.dialect.Now(), strings.Join(placeholders, ","))

		if _, err := s.db.Exec(query, args...); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// Fall back to individual updates for this chunk
			for _, id := range chunk {
				s.MarkMessageDeletedByGmailID(false, id) //nolint:errcheck,gosec // best-effort
			}
		}
	}

	return firstErr
}

// CountMessagesForSource returns the count of messages for a specific source (account).
func (s *Store) CountMessagesForSource(sourceID int64) (int64, error) {
	var count int64
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(*) FROM messages WHERE source_id = ? AND %s
	`, LiveMessagesWhere("", true)), sourceID).Scan(&count)
	return count, err
}

// CountMessagesWithRaw returns the count of messages that have raw MIME stored.
func (s *Store) CountMessagesWithRaw(sourceID int64) (int64, error) {
	var count int64
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(*) FROM messages m
		JOIN message_raw mr ON m.id = mr.message_id
		WHERE m.source_id = ? AND %s
	`, LiveMessagesWhere("m", true)), sourceID).Scan(&count)
	return count, err
}

// GetRandomMessageIDs returns a random sample of message IDs for a source.
// Uses reservoir sampling with random offsets for O(limit) performance on large tables,
// falling back to ORDER BY RANDOM() for small tables where the overhead isn't significant.
func (s *Store) GetRandomMessageIDs(sourceID int64, limit int) ([]int64, error) {
	live := LiveMessagesWhere("", true)
	// Get total count first
	var total int64
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(*) FROM messages
		WHERE source_id = ? AND %s
	`, live), sourceID).Scan(&total)
	if err != nil {
		return nil, err
	}

	if total == 0 {
		return nil, nil
	}

	// For small tables or when limit >= total, use simple ORDER BY RANDOM()
	// The threshold of 10000 balances query overhead vs. scan cost
	if total < 10000 || int64(limit) >= total {
		rows, err := s.db.Query(fmt.Sprintf(`
			SELECT id FROM messages
			WHERE source_id = ? AND %s
			ORDER BY RANDOM()
			LIMIT ?
		`, live), sourceID, limit)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()

		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	}

	// For large tables, use random offset sampling
	// This is O(limit) instead of O(n) for ORDER BY RANDOM()
	// Generate random offsets in Go for dialect portability (SQLite vs Postgres)
	// Use explicitly seeded RNG for true randomness across process runs.
	// math/rand is fine — this picks rows for sampling, not authentication.
	rng := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // sampling RNG, not security

	ids := make([]int64, 0, limit)
	seen := make(map[int64]bool)

	for len(ids) < limit {
		// Generate random offset in Go (portable across SQLite/Postgres)
		offset := rng.Int63n(total)

		var id int64
		err := s.db.QueryRow(fmt.Sprintf(`
			SELECT id FROM messages
			WHERE source_id = ? AND %s
			ORDER BY id
			LIMIT 1 OFFSET ?
		`, live), sourceID, offset).Scan(&id)
		if err != nil {
			if err == sql.ErrNoRows {
				continue // Race condition with deletions, retry
			}
			return nil, err
		}

		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	return ids, nil
}

// UpsertFTS inserts or updates the FTS index for a message.
// No-op if FTS is not available.
func (s *Store) UpsertFTS(messageID int64, subject, bodyText, fromAddr, toAddrs, ccAddrs string) error {
	if !s.fts5Available {
		return nil
	}
	return s.dialect.FTSUpsert(s.db, FTSDoc{
		MessageID: messageID,
		Subject:   subject,
		Body:      bodyText,
		FromAddr:  fromAddr,
		ToAddrs:   toAddrs,
		CcAddrs:   ccAddrs,
	})
}

// BackfillFTS populates the FTS table from existing message data.
// Processes in batches to avoid blocking for minutes on large archives.
// The progress callback (if non-nil) is called after each batch with
// (position in ID range, total ID range). Each batch is committed
// independently so partial progress is preserved if interrupted.
// Returns the number of rows inserted. No-op if FTS5 is not available.
//
// BackfillFTS clears FTS rows with DELETE before inserting. If the FTS5
// shadow tables are themselves malformed, that DELETE will either fail or
// leave corruption in place — callers recovering from shadow-table
// corruption should use RebuildFTS instead.
func (s *Store) BackfillFTS(progress func(done, total int64)) (int64, error) {
	if !s.fts5Available {
		return 0, nil
	}

	minID, maxID, err := s.messageIDRange()
	if err != nil {
		return 0, err
	}
	if maxID == 0 {
		return 0, nil
	}

	// Use the maintenance transaction for the full-index clear.
	if err := s.runMaintenance(context.Background(), func(ctx context.Context, tx *loggedTx) error {
		_, err := tx.ExecContext(ctx, s.dialect.FTSClearSQL())
		return err
	}); err != nil {
		return 0, fmt.Errorf("clear FTS: %w", err)
	}

	return s.backfillFTSRange(minID, maxID, progress)
}

// RebuildFTS fully recreates the FTS index from the underlying message
// tables. Unlike BackfillFTS (DELETE + INSERT), this drops and recreates
// the FTS table itself so malformed FTS5 shadow tables are fully replaced.
//
// Ignores the cached fts5Available flag: a corrupt shadow table causes the
// availability probe to fail, which is precisely the symptom this method
// exists to recover from. On successful completion, fts5Available is set to
// true. Returns an error if the binary was built without FTS5 support.
func (s *Store) RebuildFTS(progress func(done, total int64)) (int64, error) {
	// runMaintenance disables the pool-wide 30s statement_timeout for the
	// schema teardown/rebuild. On PG, FTSRebuildSchema runs a full-table
	// `UPDATE messages SET search_fts = NULL` (identical cost to the hatched
	// FTSClearSQL) plus a GIN rebuild over a populated table — both can exceed
	// 30s on a large archive and would cancel the rebuild-fts recovery command
	// with SQLSTATE 57014 (finding S1). On SQLite the reset SQL is "" so this
	// is an ordinary transaction around the DROP/CREATE of messages_fts.
	if err := s.runMaintenance(context.Background(), func(ctx context.Context, tx *loggedTx) error {
		return s.dialect.FTSRebuildSchema(tx)
	}); err != nil {
		return 0, err
	}

	minID, maxID, err := s.messageIDRange()
	if err != nil {
		return 0, err
	}
	if maxID == 0 {
		s.fts5Available = true
		return 0, nil
	}

	indexed, err := s.backfillFTSRange(minID, maxID, progress)
	if err != nil {
		return indexed, err
	}
	s.fts5Available = true
	return indexed, nil
}

// messageIDRange returns (minID, maxID) using MIN/MAX B-tree lookups
// rather than COUNT(*), which would scan the whole table.
func (s *Store) messageIDRange() (int64, int64, error) {
	var minID, maxID int64
	err := s.db.QueryRow(
		"SELECT COALESCE(MIN(id),0), COALESCE(MAX(id),0) FROM messages",
	).Scan(&minID, &maxID)
	if err != nil {
		return 0, 0, fmt.Errorf("get message ID range: %w", err)
	}
	return minID, maxID, nil
}

// backfillFTSRange inserts FTS rows for all messages with id in [minID, maxID],
// in batches. Shared between BackfillFTS (DELETE+fill) and RebuildFTS
// (DROP+CREATE+fill). Each batch is committed independently so partial
// progress is preserved if interrupted.
func (s *Store) backfillFTSRange(minID, maxID int64, progress func(done, total int64)) (int64, error) {
	const batchSize = 5000
	idRange := maxID - minID + 1
	var indexed int64
	cursor := minID

	for cursor <= maxID {
		batchEnd := cursor + batchSize
		n, err := s.backfillFTSBatch(cursor, batchEnd)
		if err != nil {
			return indexed, err
		}
		indexed += n
		cursor = batchEnd

		if progress != nil {
			pos := min(cursor-minID, idRange)
			progress(pos, idRange)
		}
	}
	return indexed, nil
}

// backfillFTSBatch inserts FTS rows for messages with id in [fromID, toID).
// Each batch is its own committed transaction, preserving partial progress if
// the operation is interrupted.
func (s *Store) backfillFTSBatch(fromID, toID int64) (int64, error) {
	var affected int64
	err := s.runMaintenance(context.Background(), func(ctx context.Context, tx *loggedTx) error {
		result, err := tx.ExecContext(ctx, s.dialect.FTSBackfillBatchSQL(), fromID, toID)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, err
}

func (s *Store) AttachmentPathsUniqueToSource(sourceID int64) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT a.storage_path
		FROM attachments a
		WHERE EXISTS (
		    SELECT 1 FROM messages m
		    WHERE m.id = a.message_id AND m.source_id = ?
		  )
		  AND a.content_hash IS NOT NULL
		  AND a.content_hash != ''
		  AND a.storage_path IS NOT NULL
		  AND a.storage_path != ''
		  AND a.storage_path NOT LIKE 'http://%'
		  AND a.storage_path NOT LIKE 'https://%'
		  AND NOT EXISTS (
		      SELECT 1 FROM attachments a2
		      WHERE a2.content_hash = a.content_hash
		        AND EXISTS (
		            SELECT 1 FROM messages m2
		            WHERE m2.id = a2.message_id AND m2.source_id != ?
		        )
		  )
	`, sourceID, sourceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// IsAttachmentPathReferenced returns true if any attachment record still
// points to the given storage_path. Use this immediately before deleting a
// file to guard against a concurrent sync that added a new reference after
// the candidate list was collected.
func (s *Store) IsAttachmentPathReferenced(storagePath string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM attachments WHERE storage_path = ?`,
		storagePath,
	).Scan(&count)
	if err != nil {
		return true, err // fail safe: treat error as referenced
	}
	return count > 0, nil
}

// UpsertAttachment stores an attachment record. Idempotency for the
// common case is enforced by the partial unique index
// idx_attachments_msg_content_hash on (message_id, content_hash) where
// content_hash is non-empty; concurrent inserts collapse to one row via
// ON CONFLICT DO NOTHING. `size` is widened to int64 at the bind
// boundary so 32-bit builds cannot truncate large attachments before
// the column (BIGINT on PG, INTEGER on SQLite).
//
// When contentHash is empty (the rare untyped-blob path used by some
// importers), the unique index does not cover the row; a best-effort
// (message_id, empty-hash) match is used to avoid trivial duplicates,
// but two concurrent empty-hash inserts on the same message may both
// succeed.
func (s *Store) UpsertAttachment(messageID int64, filename, mimeType, storagePath, contentHash string, size int) error {
	if contentHash != "" {
		_, err := s.db.Exec(fmt.Sprintf(`
			INSERT INTO attachments (message_id, filename, mime_type, storage_path, content_hash, size, created_at)
			VALUES (?, ?, ?, ?, ?, ?, %s)
			ON CONFLICT (message_id, content_hash) WHERE content_hash IS NOT NULL AND content_hash != '' DO NOTHING
		`, s.dialect.Now()), messageID, filename, mimeType, storagePath, contentHash, int64(size))
		return err
	}

	var existingID int64
	err := s.db.QueryRow(`
		SELECT id FROM attachments WHERE message_id = ? AND (content_hash IS NULL OR content_hash = '')
	`, messageID).Scan(&existingID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf(`
		INSERT INTO attachments (message_id, filename, mime_type, storage_path, content_hash, size, created_at)
		VALUES (?, ?, ?, ?, ?, ?, %s)
	`, s.dialect.Now()), messageID, filename, mimeType, storagePath, contentHash, int64(size))
	return err
}

// RecomputeMessageAttachmentStats refreshes the denormalized attachment flags
// on one message from its current attachment rows.
func (s *Store) RecomputeMessageAttachmentStats(messageID int64) error {
	_, err := s.db.Exec(`
		UPDATE messages
		SET has_attachments = (SELECT COUNT(*) FROM attachments WHERE message_id = ?) > 0,
		    attachment_count = (SELECT COUNT(*) FROM attachments WHERE message_id = ?)
		WHERE id = ?
	`, messageID, messageID, messageID)
	return err
}
