package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitSchemaRecordsAttachmentMigration(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	st, err := Open(dbPath)
	require.NoError(err, "Open")
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(st.InitSchema(), "first InitSchema")

	applied, err := st.IsMigrationApplied(migrationAttachmentsContentHashUnique)
	require.NoError(err, "IsMigrationApplied")
	assert.True(applied, "first InitSchema must record attachment migration")
}
