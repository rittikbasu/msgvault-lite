package store_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/store"
	"github.com/rittikbasu/msgvault-lite/internal/testutil"
)

func TestListMessagesAfterIDUsesPermanentArchiveCursor(t *testing.T) {
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "cursor@example.com")
	require.NoError(t, err)
	conversationID, err := st.EnsureConversation(source.ID, "thread-cursor", "Cursor thread")
	require.NoError(t, err)

	ids := make([]int64, 5)
	for index := range ids {
		ids[index], err = st.UpsertMessage(&store.Message{
			ConversationID:  conversationID,
			SourceID:        source.ID,
			SourceMessageID: fmt.Sprintf("cursor-message-%d", index),
			SentAt: sql.NullTime{
				Time:  time.Date(2026, 7, 1, 12, 0, index, 0, time.UTC),
				Valid: true,
			},
			Subject: sql.NullString{String: fmt.Sprintf("Message %d", index), Valid: true},
		})
		require.NoError(t, err)
	}

	firstPage, hasMore, err := st.ListMessagesAfterID(ids[0], 2)
	require.NoError(t, err)
	require.Len(t, firstPage, 2)
	assert.Equal(t, []int64{ids[1], ids[2]}, []int64{firstPage[0].ID, firstPage[1].ID})
	assert.True(t, hasMore)

	require.NoError(t, st.MarkMessageDeleted(source.ID, "cursor-message-3"))
	secondPage, hasMore, err := st.ListMessagesAfterID(ids[2], 2)
	require.NoError(t, err)
	require.Len(t, secondPage, 1)
	assert.Equal(t, ids[4], secondPage[0].ID)
	assert.False(t, hasMore)

	highWater, err := st.MessageHighWaterID()
	require.NoError(t, err)
	assert.Equal(t, ids[4], highWater)
}
