package query

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"context"
	"database/sql"
	"fmt"
	"io"
	"slices"
	"strings"

	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/store"
)

// participantNameExpr returns the SQL expression for a participant's display
// label, falling back from display_name to email_address.
func participantNameExpr(alias string) string {
	return fmt.Sprintf(
		"COALESCE(NULLIF(TRIM(%s.display_name), ''), %s.email_address)",
		alias, alias,
	)
}

// recipientNameExpr returns the SQL expression for a from/to label tied to a
// specific message_recipients row. Prefers mr.display_name (per-message Gmail
// "From: Bob <...>" override) and otherwise falls through to the participant's
// own name or email.
func recipientNameExpr(mrAlias, pAlias string) string {
	return fmt.Sprintf(
		"COALESCE(NULLIF(TRIM(%s.display_name), ''), NULLIF(TRIM(%s.display_name), ''), %s.email_address, '')",
		mrAlias, pAlias, pAlias,
	)
}

// sqliteSenderNameExpr hydrates a message summary's FromName with the same
// per-message display-name preference sender-name aggregation uses: the
// message's own "from" recipient display_name (mr_from) wins over the
// participant's sticky name, so drilling into a per-message sender-name bucket
// shows the same name the bucket was keyed by. Pairs with sqliteSenderJoin.
var sqliteSenderNameExpr = recipientNameExpr("mr_from", "p_sender")

// sqliteSenderJoin binds a message's first "from" recipient row (mr_from) and
// the resolved sender participant (p_sender), falling back to the direct
// m.sender_id participant only when the message has no "from" recipient row.
// The leading newline/indentation matches the surrounding query literals.
const sqliteSenderJoin = `LEFT JOIN message_recipients mr_from ON mr_from.id = (
			SELECT mr.id FROM message_recipients mr
			WHERE mr.message_id = m.id AND mr.recipient_type = 'from'
			ORDER BY mr.id LIMIT 1
		)
		LEFT JOIN participants p_sender ON p_sender.id = COALESCE(mr_from.participant_id, m.sender_id)`

// rebindFunc lets callers transform a query written with ? placeholders.
type rebindFunc func(string) string

// fetchLabelsForMessageList adds labels to message summaries using a batch query.
// tablePrefix optionally qualifies table names; SQLite callers pass "".
// rebind rewrites ? placeholders when needed.
func fetchLabelsForMessageList(ctx context.Context, db *sql.DB, rebind rebindFunc, tablePrefix string, messages []MessageSummary) error {
	if len(messages) == 0 {
		return nil
	}

	ids := make([]any, len(messages))
	placeholders := make([]string, len(messages))
	idToIndex := make(map[int64]int)
	for i, msg := range messages {
		ids[i] = msg.ID
		placeholders[i] = "?"
		idToIndex[msg.ID] = i
	}

	query := fmt.Sprintf(`
		SELECT ml.message_id, l.name
		FROM %smessage_labels ml
		JOIN %slabels l ON l.id = ml.label_id
		WHERE ml.message_id IN (%s)
	`, tablePrefix, tablePrefix, strings.Join(placeholders, ","))

	rows, err := db.QueryContext(ctx, rebind(query), ids...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var msgID int64
		var labelName string
		if err := rows.Scan(&msgID, &labelName); err != nil {
			return err
		}
		if idx, ok := idToIndex[msgID]; ok {
			messages[idx].Labels = append(messages[idx].Labels, labelName)
		}
	}

	return rows.Err()
}

// fetchParticipantsForMessageList adds recipients to message summaries using a batch query.
// tablePrefix optionally qualifies table names; SQLite callers pass "".
// rebind rewrites ? placeholders when needed.
func fetchParticipantsForMessageList(ctx context.Context, db *sql.DB, rebind rebindFunc, tablePrefix string, messages []MessageSummary) error {
	if len(messages) == 0 {
		return nil
	}

	ids := make([]any, len(messages))
	placeholders := make([]string, len(messages))
	idToIndex := make(map[int64]int, len(messages))
	for i, msg := range messages {
		ids[i] = msg.ID
		placeholders[i] = "?"
		idToIndex[msg.ID] = i
	}

	rows, err := db.QueryContext(ctx, rebind(fmt.Sprintf(`
		SELECT mr.message_id,
		       mr.recipient_type,
		       COALESCE(NULLIF(p.email_address, ''), ''),
		       %s
		FROM %smessage_recipients mr
		JOIN %sparticipants p ON p.id = mr.participant_id
		WHERE mr.message_id IN (%s)
		  AND mr.recipient_type IN ('to', 'cc', 'bcc')
		ORDER BY mr.message_id, mr.id
	`, recipientNameExpr("mr", "p"), tablePrefix, tablePrefix, strings.Join(placeholders, ","))), ids...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var messageID int64
		var recipType, email, name string
		if err := rows.Scan(&messageID, &recipType, &email, &name); err != nil {
			return err
		}
		idx, ok := idToIndex[messageID]
		if !ok {
			continue
		}
		appendSummaryRecipient(&messages[idx], recipType, Address{Email: email, Name: name})
	}

	return rows.Err()
}

func appendSummaryRecipient(msg *MessageSummary, recipType string, addr Address) {
	switch recipType {
	case "to":
		msg.To = append(msg.To, addr)
	case "cc":
		msg.Cc = append(msg.Cc, addr)
	case "bcc":
		msg.Bcc = append(msg.Bcc, addr)
	}
}

// fetchMessageLabelsDetail fetches labels for a single message detail.
// tablePrefix optionally qualifies table names; SQLite callers pass "".
// rebind rewrites ? placeholders when needed.
func fetchMessageLabelsDetail(ctx context.Context, db *sql.DB, rebind rebindFunc, tablePrefix string, msg *MessageDetail) error {
	rows, err := db.QueryContext(ctx, rebind(fmt.Sprintf(`
		SELECT l.name
		FROM %smessage_labels ml
		JOIN %slabels l ON l.id = ml.label_id
		WHERE ml.message_id = ?
	`, tablePrefix, tablePrefix)), msg.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		msg.Labels = append(msg.Labels, name)
	}

	return rows.Err()
}

// fetchParticipantsShared fetches participants for a single message detail.
// tablePrefix optionally qualifies table names; SQLite callers pass "".
// rebind rewrites ? placeholders when needed.
func fetchParticipantsShared(ctx context.Context, db *sql.DB, rebind rebindFunc, tablePrefix string, msg *MessageDetail) error {
	rows, err := db.QueryContext(ctx, rebind(fmt.Sprintf(`
		SELECT mr.recipient_type, COALESCE(NULLIF(p.email_address, ''), ''), %s
		FROM %smessage_recipients mr
		JOIN %sparticipants p ON p.id = mr.participant_id
		WHERE mr.message_id = ?
	`, recipientNameExpr("mr", "p"), tablePrefix, tablePrefix)), msg.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var recipType, email, name string
		if err := rows.Scan(&recipType, &email, &name); err != nil {
			return err
		}
		addr := Address{Email: email, Name: name}
		switch recipType {
		case "from":
			msg.From = append(msg.From, addr)
		case "to":
			msg.To = append(msg.To, addr)
		case "cc":
			msg.Cc = append(msg.Cc, addr)
		case "bcc":
			msg.Bcc = append(msg.Bcc, addr)
		}
	}

	return rows.Err()
}

// fetchAttachmentsShared fetches attachments for a single message detail.
// tablePrefix optionally qualifies table names; SQLite callers pass "".
// rebind rewrites ? placeholders when needed.
func fetchAttachmentsShared(ctx context.Context, db *sql.DB, rebind rebindFunc, tablePrefix string, msg *MessageDetail) error {
	rows, err := db.QueryContext(ctx, rebind(fmt.Sprintf(`
		SELECT id, COALESCE(filename, ''), COALESCE(mime_type, ''), COALESCE(size, 0), COALESCE(content_hash, ''), COALESCE(storage_path, '')
		FROM %sattachments
		WHERE message_id = ?
	`, tablePrefix)), msg.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var att AttachmentInfo
		var storagePath string
		if err := rows.Scan(&att.ID, &att.Filename, &att.MimeType, &att.Size, &att.ContentHash, &storagePath); err != nil {
			return err
		}
		if isURLStoragePath(storagePath) {
			att.URL = storagePath
			att.ContentHash = ""
		}
		msg.Attachments = append(msg.Attachments, att)
	}

	return rows.Err()
}

func isURLStoragePath(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}

// extractBodyFromRawShared extracts text body from compressed MIME data.
// tablePrefix optionally qualifies table names; SQLite callers pass "".
// rebind rewrites ? placeholders when needed.
func extractBodyFromRawShared(ctx context.Context, db *sql.DB, rebind rebindFunc, tablePrefix string, messageID int64) (string, error) {
	var compressed []byte
	var compression sql.NullString

	err := db.QueryRowContext(ctx, rebind(fmt.Sprintf(`
		SELECT raw_data, compression FROM %smessage_raw WHERE message_id = ?
	`, tablePrefix)), messageID).Scan(&compressed, &compression)
	if err != nil {
		return "", err
	}

	var rawData []byte
	if compression.Valid && compression.String == "zlib" {
		r, err := zlib.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return "", fmt.Errorf("open zlib reader for raw message: %w", err)
		}
		defer func() { _ = r.Close() }()
		rawData, err = io.ReadAll(r)
		if err != nil {
			return "", err
		}
	} else {
		rawData = compressed
	}

	parsed, err := mime.Parse(rawData)
	if err != nil {
		return "", err
	}

	return parsed.GetBodyText(), nil
}

// getMessageRawShared retrieves and decompresses raw MIME data for a message.
// Returns nil, nil if no raw data is stored, or if the message is hidden from
// normal reads — source-deleted rows
// (deleted_from_source_at), matching the visibility rule
// the list/search endpoints apply via store.LiveMessagesWhere.
func getMessageRawShared(ctx context.Context, db *sql.DB, rebind rebindFunc, tablePrefix string, messageID int64) ([]byte, error) {
	var compressed []byte
	var compression sql.NullString

	err := db.QueryRowContext(ctx, rebind(fmt.Sprintf(`
		SELECT mr.raw_data, mr.compression
		FROM %smessage_raw mr
		JOIN %smessages m ON m.id = mr.message_id
		WHERE mr.message_id = ? AND %s
	`, tablePrefix, tablePrefix, store.LiveMessagesWhere("m", true))), messageID).Scan(&compressed, &compression)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query message_raw for id %d: %w", messageID, err)
	}

	if compression.Valid && compression.String == "zlib" {
		r, err := zlib.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, fmt.Errorf("zlib reader for id %d: %w", messageID, err)
		}
		defer func() { _ = r.Close() }()
		raw, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("zlib decompress message_raw id %d: %w", messageID, err)
		}
		return raw, nil
	}

	return compressed, nil
}

// getMessageByQueryShared retrieves a full message detail by an arbitrary WHERE clause.
// tablePrefix optionally qualifies table names; SQLite callers pass "".
// rebind rewrites ? placeholders when needed; it is applied
// to every sub-query this function dispatches.
func getMessageByQueryShared(ctx context.Context, db *sql.DB, rebind rebindFunc, tablePrefix string, whereClause string, args ...any) (*MessageDetail, error) {
	query := fmt.Sprintf(`
		SELECT
			m.id,
			m.source_message_id,
			m.conversation_id,
			COALESCE(conv.source_conversation_id, ''),
			COALESCE(m.subject, ''),
			COALESCE(m.snippet, ''),
			m.sent_at,
			COALESCE(m.size_estimate, 0),
			m.has_attachments,
			m.deleted_from_source_at
		FROM %smessages m
		LEFT JOIN %sconversations conv ON conv.id = m.conversation_id
		WHERE %s
	`, tablePrefix, tablePrefix, whereClause)

	var msg MessageDetail
	var sentAt, deletedAt sql.NullTime
	err := db.QueryRowContext(ctx, rebind(query), args...).Scan(
		&msg.ID,
		&msg.SourceMessageID,
		&msg.ConversationID,
		&msg.SourceConversationID,
		&msg.Subject,
		&msg.Snippet,
		&sentAt,
		&msg.SizeEstimate,
		&msg.HasAttachments,
		&deletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil //nolint:nilnil // Engine.GetMessage/GetMessageBySourceID use (nil, nil) for not-found; callers chain fallback lookups on the nil result
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}

	if sentAt.Valid {
		msg.SentAt = sentAt.Time
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		msg.DeletedAt = &t
	}

	// Fetch body from separate table (PK lookup, avoids scanning large body B-tree)
	var bodyText, bodyHTML sql.NullString
	err = db.QueryRowContext(ctx, rebind(fmt.Sprintf(`
		SELECT body_text, body_html FROM %smessage_bodies WHERE message_id = ?
	`, tablePrefix)), msg.ID).Scan(&bodyText, &bodyHTML)
	if err == nil {
		if bodyText.Valid {
			msg.BodyText = bodyText.String
		}
		if bodyHTML.Valid {
			msg.BodyHTML = bodyHTML.String
		}
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("get message body: %w", err)
	}

	// If body is empty, try to extract from raw MIME
	if msg.BodyText == "" && msg.BodyHTML == "" {
		if body, err := extractBodyFromRawShared(ctx, db, rebind, tablePrefix, msg.ID); err == nil && body != "" {
			msg.BodyText = body
		}
	}

	// Fetch participants
	if err := fetchParticipantsShared(ctx, db, rebind, tablePrefix, &msg); err != nil {
		return nil, fmt.Errorf("fetch participants: %w", err)
	}

	// Fetch labels
	if err := fetchMessageLabelsDetail(ctx, db, rebind, tablePrefix, &msg); err != nil {
		return nil, fmt.Errorf("fetch labels: %w", err)
	}

	// Fetch attachments
	if err := fetchAttachmentsShared(ctx, db, rebind, tablePrefix, &msg); err != nil {
		return nil, fmt.Errorf("fetch attachments: %w", err)
	}

	return &msg, nil
}

// collectGmailIDs scans rows for source_message_id strings.
func collectGmailIDs(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan gmail id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate gmail ids: %w", err)
	}
	return ids, nil
}

// gmailIDRow carries the ORDER BY keys (sent_at, message id) alongside a
// source_message_id so chunked message-ID resolution can be merged back
// into the single-query newest-first order.
type gmailIDRow struct {
	gmailID string
	sentAt  sql.NullTime
	id      int64
}

// collectGmailIDRows scans (source_message_id, sent_at, id) rows.
func collectGmailIDRows(rows *sql.Rows) ([]gmailIDRow, error) {
	defer func() { _ = rows.Close() }()
	var out []gmailIDRow
	for rows.Next() {
		var r gmailIDRow
		if err := rows.Scan(&r.gmailID, &r.sentAt, &r.id); err != nil {
			return nil, fmt.Errorf("scan gmail id row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate gmail id rows: %w", err)
	}
	return out, nil
}

// gmailIDsByMessageIDsChunked resolves message IDs to Gmail IDs in
// inListChunkSize batches so arbitrarily large explicit selections stay
// under the backend's bind-parameter limit. Input IDs are deduplicated
// (each message row surfaces from exactly one chunk) and the merged
// result is re-sorted to the single-query contract: sent_at DESC,
// id DESC, with NULL sent_at last.
func gmailIDsByMessageIDsChunked(
	ctx context.Context,
	ids []int64,
	queryChunk func(ctx context.Context, chunk []int64) ([]gmailIDRow, error),
) ([]string, error) {
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	var merged []gmailIDRow
	for start := 0; start < len(unique); start += inListChunkSize {
		end := min(start+inListChunkSize, len(unique))
		rows, err := queryChunk(ctx, unique[start:end])
		if err != nil {
			return nil, err
		}
		merged = append(merged, rows...)
	}

	slices.SortFunc(merged, compareGmailIDRowsNewestFirst)
	out := make([]string, len(merged))
	for i, r := range merged {
		out[i] = r.gmailID
	}
	return out, nil
}

// compareGmailIDRowsNewestFirst orders by sent_at DESC then id DESC,
// with NULL sent_at sorting last (matching SQLite DESC semantics).
func compareGmailIDRowsNewestFirst(a, b gmailIDRow) int {
	switch {
	case a.sentAt.Valid && !b.sentAt.Valid:
		return -1
	case !a.sentAt.Valid && b.sentAt.Valid:
		return 1
	case a.sentAt.Valid && b.sentAt.Valid && !a.sentAt.Time.Equal(b.sentAt.Time):
		if a.sentAt.Time.After(b.sentAt.Time) {
			return -1
		}
		return 1
	default:
		return cmp.Compare(b.id, a.id)
	}
}
