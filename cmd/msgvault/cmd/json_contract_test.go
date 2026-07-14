package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

func decodeJSONObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	return got
}

func TestStatusJSONContract(t *testing.T) {
	seedDirectCLIArchive(t)
	oldJSON := statsJSON
	statsJSON = true
	t.Cleanup(func() { statsJSON = oldJSON })

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	require.NoError(t, runStats(cmd, nil))

	got := decodeJSONObject(t, out.Bytes())
	assert.Equal(t, float64(jsonSchemaVersion), got["schema_version"])
	messages := got["messages"].(map[string]any)
	assert.Equal(t, float64(1), messages["total"])
	assert.Equal(t, float64(1), messages["active"])
	assert.Equal(t, float64(0), messages["deleted_from_source"])
}

func TestMessagesJSONContract(t *testing.T) {
	_, messageID := seedDirectCLIArchive(t)
	oldLimit, oldOffset, oldJSON := messagesLimit, messagesOffset, messagesJSON
	messagesLimit, messagesOffset, messagesJSON = 10, 0, true
	t.Cleanup(func() {
		messagesLimit, messagesOffset, messagesJSON = oldLimit, oldOffset, oldJSON
	})

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	require.NoError(t, runMessages(cmd))

	got := decodeJSONObject(t, out.Bytes())
	assert.Equal(t, float64(jsonSchemaVersion), got["schema_version"])
	items := got["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, float64(messageID), items[0].(map[string]any)["id"])
	page := got["page"].(map[string]any)
	assert.Equal(t, float64(10), page["limit"])
	assert.Equal(t, float64(0), page["offset"])
	assert.Equal(t, float64(1), page["returned"])
	assert.Equal(t, float64(1), page["total"])
	assert.Equal(t, false, page["has_more"])
}

func TestSearchJSONContract(t *testing.T) {
	_, messageID := seedDirectCLIArchive(t)
	oldLimit, oldOffset, oldJSON := searchLimit, searchOffset, searchJSON
	searchLimit, searchOffset, searchJSON = 1, 0, true
	t.Cleanup(func() {
		searchLimit, searchOffset, searchJSON = oldLimit, oldOffset, oldJSON
	})

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	require.NoError(t, runSearch(cmd, "subject:Needle"))

	got := decodeJSONObject(t, out.Bytes())
	assert.Equal(t, float64(jsonSchemaVersion), got["schema_version"])
	items := got["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, float64(messageID), items[0].(map[string]any)["id"])
	page := got["page"].(map[string]any)
	assert.Equal(t, float64(1), page["limit"])
	assert.Equal(t, float64(1), page["returned"])
	assert.Equal(t, false, page["has_more"])
	assert.NotContains(t, page, "total")
}

func TestSearchJSONContractReportsHasMore(t *testing.T) {
	dataDir, _ := seedDirectCLIArchive(t)
	st, err := store.Open(filepath.Join(dataDir, "msgvault.db"))
	require.NoError(t, err)
	source, err := st.GetOrCreateSource("gmail", "user@example.com")
	require.NoError(t, err)
	conversationID, err := st.EnsureConversation(source.ID, "thread-2", "Needle thread 2")
	require.NoError(t, err)
	_, err = st.UpsertMessage(&store.Message{
		ConversationID:  conversationID,
		SourceID:        source.ID,
		SourceMessageID: "gmail-needle-2",
		SentAt:          sql.NullTime{Time: time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC), Valid: true},
		Subject:         sql.NullString{String: "Needle subject 2", Valid: true},
	})
	require.NoError(t, err)
	require.NoError(t, st.Close())

	oldLimit, oldOffset, oldJSON := searchLimit, searchOffset, searchJSON
	searchLimit, searchOffset, searchJSON = 1, 0, true
	t.Cleanup(func() {
		searchLimit, searchOffset, searchJSON = oldLimit, oldOffset, oldJSON
	})

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	require.NoError(t, runSearch(cmd, "subject:Needle"))

	got := decodeJSONObject(t, out.Bytes())
	assert.Len(t, got["items"].([]any), 1)
	page := got["page"].(map[string]any)
	assert.Equal(t, float64(1), page["returned"])
	assert.Equal(t, true, page["has_more"])
}

func TestShowJSONContractBoundsBodies(t *testing.T) {
	msg := &query.MessageDetail{
		ID:       42,
		SentAt:   time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC),
		BodyText: strings.Repeat("é", 10),
		BodyHTML: strings.Repeat("x", 20),
	}
	var out bytes.Buffer
	require.NoError(t, outputMessageJSON(&out, msg, 9))

	got := decodeJSONObject(t, out.Bytes())
	assert.Equal(t, float64(jsonSchemaVersion), got["schema_version"])
	message := got["message"].(map[string]any)
	assert.Equal(t, float64(42), message["id"])
	assert.Equal(t, "éééé", message["body_text"])
	assert.Equal(t, true, message["body_text_truncated"])
	assert.Equal(t, "xxxxxxxxx", message["body_html"])
	assert.Equal(t, true, message["body_html_truncated"])
}

func TestShowTextUsesProvidedWriter(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, outputMessageText(&out, &query.MessageDetail{
		ID:              42,
		SourceMessageID: "gmail-42",
		Subject:         "Writer test",
		BodyText:        "body",
	}))
	assert.Contains(t, out.String(), "Message ID: 42")
	assert.Contains(t, out.String(), "Writer test")
	assert.Contains(t, out.String(), "body")
}

func TestBoundedUTF8NormalizesBeforeApplyingByteLimit(t *testing.T) {
	got, truncated := boundedUTF8("a\xffb", 4)
	assert.Equal(t, "a�", got)
	assert.Len(t, []byte(got), 4)
	assert.True(t, truncated)
}

func TestJSONPageLimitsAreBounded(t *testing.T) {
	oldMessagesLimit, oldSearchLimit := messagesLimit, searchLimit
	oldBodyLimit := showMessageMaxBodyBytes
	t.Cleanup(func() {
		messagesLimit, searchLimit = oldMessagesLimit, oldSearchLimit
		showMessageMaxBodyBytes = oldBodyLimit
	})

	messagesLimit = maxJSONPageSize + 1
	err := messagesCmd.RunE(&cobra.Command{}, nil)
	require.ErrorContains(t, err, "must not exceed")

	searchLimit = maxJSONPageSize + 1
	err = searchCmd.RunE(&cobra.Command{}, []string{"needle"})
	require.ErrorContains(t, err, "must not exceed")

	showMessageMaxBodyBytes = maxJSONBodyBytes + 1
	err = showMessageCmd.RunE(&cobra.Command{}, []string{"1"})
	require.ErrorContains(t, err, "must not exceed")
}
