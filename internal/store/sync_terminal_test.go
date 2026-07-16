package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/testutil/storetest"
)

func TestCompleteSyncRejectsSupersededRun(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)
	firstID := f.StartSync()

	_, err := f.Store.StartSync(f.Source.ID, "incremental")
	require.NoError(err, "start superseding sync")

	err = f.Store.CompleteSync(firstID, "history-1")
	require.Error(err, "superseded sync must not be completed")
	require.ErrorContains(err, "running sync run")

	var status string
	err = f.Store.DB().QueryRow(`SELECT status FROM sync_runs WHERE id = ?`, firstID).Scan(&status)
	require.NoError(err, "read superseded run")
	assert.Equal("failed", status, "superseded run must remain failed")
}
