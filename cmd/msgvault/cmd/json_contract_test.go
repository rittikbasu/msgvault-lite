package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/query"
	"github.com/rittikbasu/msgvault-lite/internal/store"
)

func decodeJSONObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var got map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&got))
	return got
}

func requireJSONObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok, "value is not a JSON object: %T", value)
	return object
}

func requireJSONArray(t *testing.T, value any) []any {
	t.Helper()
	array, ok := value.([]any)
	require.True(t, ok, "value is not a JSON array: %T", value)
	return array
}

func jsonInt(value int64) json.Number {
	return json.Number(strconv.FormatInt(value, 10))
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
	assert.Equal(t, jsonInt(jsonSchemaVersion), got["schema_version"])
	messages := requireJSONObject(t, got["messages"])
	assert.Equal(t, jsonInt(1), messages["total"])
	assert.Equal(t, jsonInt(1), messages["active"])
	assert.Equal(t, jsonInt(0), messages["deleted_from_source"])
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
	assert.Equal(t, jsonInt(jsonSchemaVersion), got["schema_version"])
	items := requireJSONArray(t, got["items"])
	require.Len(t, items, 1)
	assert.Equal(t, jsonInt(messageID), requireJSONObject(t, items[0])["id"])
	page := requireJSONObject(t, got["page"])
	assert.Equal(t, jsonInt(10), page["limit"])
	assert.Equal(t, jsonInt(0), page["offset"])
	assert.Equal(t, jsonInt(1), page["returned"])
	assert.Equal(t, jsonInt(1), page["total"])
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
	assert.Equal(t, jsonInt(jsonSchemaVersion), got["schema_version"])
	items := requireJSONArray(t, got["items"])
	require.Len(t, items, 1)
	assert.Equal(t, jsonInt(messageID), requireJSONObject(t, items[0])["id"])
	page := requireJSONObject(t, got["page"])
	assert.Equal(t, jsonInt(1), page["limit"])
	assert.Equal(t, jsonInt(1), page["returned"])
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
	assert.Len(t, requireJSONArray(t, got["items"]), 1)
	page := requireJSONObject(t, got["page"])
	assert.Equal(t, jsonInt(1), page["returned"])
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
	assert.Equal(t, jsonInt(jsonSchemaVersion), got["schema_version"])
	message := requireJSONObject(t, got["message"])
	assert.Equal(t, jsonInt(42), message["id"])
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
