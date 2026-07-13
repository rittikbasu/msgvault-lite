package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitSchemaKeepsMessageIDsAboveLegacyHighWater(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-message-ids.db")
	legacy, err := schemaFS.ReadFile("schema.sql")
	require.NoError(t, err, "read schema")
	legacy = []byte(strings.Replace(
		string(legacy),
		"id INTEGER PRIMARY KEY AUTOINCREMENT,",
		"id INTEGER PRIMARY KEY,",
		1,
	))

	seed, err := Open(dbPath)
	require.NoError(t, err, "open legacy database")
	_, err = seed.DB().Exec(string(legacy))
	require.NoError(t, err, "create legacy schema")
	_, err = seed.DB().Exec(`DROP TRIGGER messages_reject_delete`)
	require.NoError(t, err, "drop future delete guard")
	_, err = seed.DB().Exec(`
		INSERT INTO sources (id, source_type, identifier) VALUES (1, 'gmail', 'legacy@example.com');
		INSERT INTO conversations (id, source_id, source_conversation_id, conversation_type)
		VALUES (1, 1, 'legacy-thread', 'email_thread');
		INSERT INTO messages (id, conversation_id, source_id, source_message_id, message_type)
		VALUES (41, 1, 1, 'legacy-41', 'email'), (42, 1, 1, 'legacy-42', 'email');
		DELETE FROM messages WHERE id = 42;
	`)
	require.NoError(t, err, "seed legacy deletion")
	require.NoError(t, seed.Close(), "close legacy database")

	st, err := Open(dbPath)
	require.NoError(t, err, "reopen legacy database")
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.InitSchema(), "migrate legacy database")

	id, err := st.UpsertMessage(&Message{
		SourceID:        1,
		ConversationID:  1,
		SourceMessageID: "post-migration",
		MessageType:     "email",
		Subject:         sql.NullString{String: "post migration", Valid: true},
	})
	require.NoError(t, err, "UpsertMessage")
	assert.Greater(t, id, int64(42), "upgraded archive must not reuse a historical message ID")
}
