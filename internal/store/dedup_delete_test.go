package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestDeleteDedupedBatchRefusesPhysicalDelete(t *testing.T) {
	f := storetest.New(t)
	idKeep := newRFC822Message(t, f, "keep", "rfc822-delete-a")
	idDrop := newRFC822Message(t, f, "drop", "rfc822-delete-a")

	labels := f.EnsureLabels(map[string]string{"INBOX": "Inbox"}, "system")
	require.NoError(t, f.Store.LinkMessageLabel(idDrop, labels["INBOX"]))
	_, err := f.Store.MergeDuplicates(idKeep, []int64{idDrop}, "batch-delete")
	require.NoError(t, err)

	deleted, err := f.Store.DeleteDedupedBatch("batch-delete")
	assert.ErrorIs(t, err, store.ErrMessagesInsertOnly)
	assert.Equal(t, int64(0), deleted)

	var messages, messageLabels int
	require.NoError(t, f.Store.DB().QueryRow(
		f.Store.Rebind("SELECT COUNT(*) FROM messages WHERE id = ?"), idDrop,
	).Scan(&messages))
	require.NoError(t, f.Store.DB().QueryRow(
		f.Store.Rebind("SELECT COUNT(*) FROM message_labels WHERE message_id = ?"), idDrop,
	).Scan(&messageLabels))
	assert.Equal(t, 1, messages, "dedup-hidden message must remain archived")
	assert.Equal(t, 1, messageLabels, "archived child rows must remain intact")
}

func TestDeleteAllDedupedRefusesPhysicalDelete(t *testing.T) {
	f := storetest.New(t)
	idKeep := newRFC822Message(t, f, "keep", "rfc822-delete-all")
	idDrop := newRFC822Message(t, f, "drop", "rfc822-delete-all")
	_, err := f.Store.MergeDuplicates(idKeep, []int64{idDrop}, "batch-delete-all")
	require.NoError(t, err)

	deleted, batches, err := f.Store.DeleteAllDeduped()
	assert.ErrorIs(t, err, store.ErrMessagesInsertOnly)
	assert.Equal(t, int64(0), deleted)
	assert.Equal(t, int64(0), batches)

	var messages int
	require.NoError(t, f.Store.DB().QueryRow("SELECT COUNT(*) FROM messages").Scan(&messages))
	assert.Equal(t, 2, messages, "all message IDs must remain archived")
}
