package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/testutil/storetest"
)

// setDeletedFromSource marks a message as deleted from its source account
// (retained in the archive).
func setDeletedFromSource(f *storetest.Fixture, id int64) {
	f.T.Helper()
	_, err := f.Store.DB().Exec(
		f.Store.Rebind("UPDATE messages SET deleted_from_source_at = CURRENT_TIMESTAMP WHERE id = ?"),
		id,
	)
	require.NoError(f.T, err, "set deleted_from_source_at")
}

// TestStore_StatsSourceDeletedBreakdown asserts that GetStats splits the
// archived population into active and source-deleted counts, that the two are
// exact complements of the archived message population.
func TestStore_StatsSourceDeletedBreakdown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := storetest.New(t)

	f.CreateMessages(5) // 5 active messages

	sourceDeleted := []int64{
		f.CreateMessage("src-del-1"),
		f.CreateMessage("src-del-2"),
		f.CreateMessage("src-del-3"),
	}
	for _, id := range sourceDeleted {
		setDeletedFromSource(f, id)
	}

	stats, err := f.Store.GetStats()
	require.NoError(err, "GetStats")

	assert.Equal(int64(5), stats.MessageCount, "active MessageCount")
	assert.Equal(int64(3), stats.SourceDeletedCount, "SourceDeletedCount")

	assert.Equal(int64(8), stats.MessageCount+stats.SourceDeletedCount, "canonical total")
}
