package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/store"
	"github.com/rittikbasu/msgvault-lite/internal/testutil"
)

func TestLiveMessageSourceIDsScopesSourceAndExcludesTombstones(t *testing.T) {
	t.Parallel()

	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "user@example.com")
	require.NoError(t, err)
	otherSource, err := st.GetOrCreateSource("gmail", "other@example.com")
	require.NoError(t, err)
	conversationID, err := st.EnsureConversation(source.ID, "thread", "thread")
	require.NoError(t, err)
	otherConversationID, err := st.EnsureConversation(otherSource.ID, "other-thread", "other thread")
	require.NoError(t, err)

	for _, message := range []*store.Message{
		{ConversationID: conversationID, SourceID: source.ID, SourceMessageID: "live"},
		{ConversationID: conversationID, SourceID: source.ID, SourceMessageID: "deleted"},
		{ConversationID: otherConversationID, SourceID: otherSource.ID, SourceMessageID: "other"},
	} {
		_, err := st.UpsertMessage(message)
		require.NoError(t, err)
	}
	require.NoError(t, st.MarkMessageDeletedByGmailID(false, "deleted"))

	ids, err := st.LiveMessageSourceIDs(source.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"live"}, ids)
}
