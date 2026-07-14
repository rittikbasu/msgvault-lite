package store_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func retiredLastModifiedSchemaCounts(t *testing.T, st *store.Store) (columns, triggers int) {
	t.Helper()
	require.NoError(t, st.DB().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'last_modified'`,
	).Scan(&columns), "count last_modified columns")
	require.NoError(t, st.DB().QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'trigger'
		  AND name IN (
			'trg_messages_last_modified',
			'trg_message_bodies_last_modified_upd',
			'trg_message_bodies_last_modified_ins'
		)
	`).Scan(&triggers), "count retired last_modified triggers")
	return columns, triggers
}

func TestFreshArchiveOmitsRetiredLastModifiedBehavior(t *testing.T) {
	st, err := store.OpenForTest(filepath.Join(t.TempDir(), "fresh.db"))
	require.NoError(t, err, "OpenForTest")
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.InitSchema(), "InitSchema")

	columns, triggers := retiredLastModifiedSchemaCounts(t, st)
	assert.Zero(t, columns, "fresh archives must not create the retired vector watermark")
	assert.Zero(t, triggers, "fresh archives must not create retired vector triggers")
}

func TestInitSchemaDropsRetiredLastModifiedTriggers(t *testing.T) {
	st, err := store.OpenForTest(filepath.Join(t.TempDir(), "upgrade.db"))
	require.NoError(t, err, "OpenForTest")
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.InitSchema(), "initial InitSchema")

	columns, _ := retiredLastModifiedSchemaCounts(t, st)
	if columns == 0 {
		_, err = st.DB().Exec(`ALTER TABLE messages ADD COLUMN last_modified DATETIME`)
		require.NoError(t, err, "add retired compatibility column")
	}
	_, err = st.DB().Exec(`
		CREATE TRIGGER IF NOT EXISTS trg_messages_last_modified
		AFTER UPDATE ON messages BEGIN
			UPDATE messages SET last_modified = CURRENT_TIMESTAMP WHERE id = NEW.id;
		END;
		CREATE TRIGGER IF NOT EXISTS trg_message_bodies_last_modified_upd
		AFTER UPDATE ON message_bodies BEGIN
			UPDATE messages SET last_modified = CURRENT_TIMESTAMP WHERE id = NEW.message_id;
		END;
		CREATE TRIGGER IF NOT EXISTS trg_message_bodies_last_modified_ins
		AFTER INSERT ON message_bodies BEGIN
			UPDATE messages SET last_modified = CURRENT_TIMESTAMP WHERE id = NEW.message_id;
		END;
	`)
	require.NoError(t, err, "create retired triggers")

	require.NoError(t, st.InitSchema(), "upgrade InitSchema")
	columns, triggers := retiredLastModifiedSchemaCounts(t, st)
	assert.Equal(t, 1, columns, "upgrades must preserve the physical compatibility column")
	assert.Zero(t, triggers, "upgrades must remove retired vector triggers")
}
