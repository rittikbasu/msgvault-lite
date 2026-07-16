package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/testutil"
)

func TestFreshArchiveUsesForkNativeSchema(t *testing.T) {
	f := testutil.NewTestStore(t)
	assert := assert.New(t)

	var schemaVersion int
	require.NoError(t, f.DB().QueryRow(`PRAGMA user_version`).Scan(&schemaVersion))
	assert.Equal(1, schemaVersion, "fork schema version")

	for _, table := range []string{
		"account_identities",
		"applied_migrations",
		"participant_identifiers",
		"conversation_participants",
		"reactions",
		"imap_folder_state",
		"source_import_items",
		"collections",
		"collection_sources",
	} {
		var count int
		require.NoError(t, f.DB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&count))
		assert.Zero(count, "retired table %s", table)
	}

	for _, column := range []string{"embed_gen", "last_modified", "deleted_at", "delete_batch_id"} {
		var count int
		require.NoError(t, f.DB().QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = ?`, column,
		).Scan(&count))
		assert.Zero(count, "retired messages column %s", column)
	}
}

func TestFreshArchiveRemovedCompatibilityColumns(t *testing.T) {
	f := testutil.NewTestStore(t)

	removed := map[string][]string{
		"sources": {
			"sync_config",
		},
		"participants": {
			"phone_number", "canonical_id",
		},
		"conversations": {
			"conversation_type", "participant_count", "unread_count",
			"last_message_at", "last_message_preview", "metadata",
		},
		"messages": {
			"rfc822_message_id", "message_type", "received_at", "read_at",
			"delivered_at", "is_from_me", "is_read", "is_delivered", "is_sent",
			"is_edited", "is_forwarded", "reply_to_message_id", "thread_position",
			"indexing_version", "metadata",
		},
		"attachments": {
			"media_type", "width", "height", "duration_ms", "thumbnail_hash",
			"thumbnail_path", "attachment_metadata", "encryption_version",
		},
		"message_raw": {
			"encryption_version",
		},
		"labels": {
			"color",
		},
	}

	for table, columns := range removed {
		for _, column := range columns {
			var count int
			require.NoError(t, f.DB().QueryRow(
				`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
			).Scan(&count))
			assert.Zero(t, count, "%s.%s should not exist", table, column)
		}
	}

	_, err := f.DB().Exec(`INSERT INTO sources (id, source_type, identifier) VALUES (1, 'gmail', 'first@example.com')`)
	require.NoError(t, err)
	_, err = f.DB().Exec(`INSERT INTO participants (id, email_address) VALUES (1, 'sender@example.com')`)
	require.NoError(t, err)
	_, err = f.DB().Exec(`INSERT INTO conversations (id, source_id, source_conversation_id) VALUES (1, 1, 'thread-1')`)
	require.NoError(t, err)
	_, err = f.DB().Exec(`INSERT INTO messages (id, conversation_id, source_id, source_message_id) VALUES (1, 1, 1, 'message-1')`)
	require.NoError(t, err)

	_, err = f.DB().Exec(`INSERT INTO sources (source_type, identifier) VALUES ('imap', 'imap@example.com')`)
	require.Error(t, err)
	require.ErrorContains(t, err, "CHECK constraint failed")

	_, err = f.DB().Exec(`INSERT INTO message_recipients (message_id, participant_id, recipient_type) VALUES (1, 1, 'mention')`)
	require.Error(t, err)
	require.ErrorContains(t, err, "CHECK constraint failed")
}
