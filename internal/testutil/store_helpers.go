package testutil

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/store"
)

// NewTestStore creates a temporary database for testing.
// The database is automatically cleaned up when the test completes.
func NewTestStore(t *testing.T) *store.Store {
	t.Helper()
	return NewSQLiteTestStore(t)
}

// NewSQLiteTestStore creates a temporary SQLite store.
func NewSQLiteTestStore(t *testing.T) *store.Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.OpenForTest(dbPath)
	require.NoError(t, err, "open store")

	t.Cleanup(func() {
		_ = st.Close()
	})

	require.NoError(t, st.InitSchema(), "init schema")

	return st
}
