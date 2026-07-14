package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil"
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
