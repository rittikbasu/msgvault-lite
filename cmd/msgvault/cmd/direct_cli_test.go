package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
)

func seedDirectCLIArchive(t *testing.T) (string, int64) {
	t.Helper()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "msgvault.db")
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.InitSchema())

	source, err := st.GetOrCreateSource("gmail", "user@example.com")
	require.NoError(t, err)
	conversationID, err := st.EnsureConversation(source.ID, "thread-1", "Needle thread")
	require.NoError(t, err)
	messageID, err := st.UpsertMessage(&store.Message{
		ConversationID:  conversationID,
		SourceID:        source.ID,
		SourceMessageID: "gmail-needle-1",
		MessageType:     "email",
		SentAt:          sql.NullTime{Time: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), Valid: true},
		Subject:         sql.NullString{String: "Needle subject", Valid: true},
		Snippet:         sql.NullString{String: "needle preview", Valid: true},
		SizeEstimate:    1234,
	})
	require.NoError(t, err)
	require.NoError(t, st.UpsertMessageBody(messageID,
		sql.NullString{String: "needle body", Valid: true}, sql.NullString{}))
	require.NoError(t, st.Close())

	withStoreResolverConfig(t, &config.Config{
		HomeDir: dataDir,
		Data:    config.DataConfig{DataDir: dataDir},
	})
	return dataDir, messageID
}

func TestSearchReadsSQLiteDirectly(t *testing.T) {
	_, messageID := seedDirectCLIArchive(t)
	oldLimit, oldOffset, oldJSON := searchLimit, searchOffset, searchJSON
	oldAccount, oldCollection := searchAccount, searchCollection
	oldTypes := append([]string(nil), searchMessageTypes...)
	t.Cleanup(func() {
		searchLimit, searchOffset, searchJSON = oldLimit, oldOffset, oldJSON
		searchAccount, searchCollection = oldAccount, oldCollection
		searchMessageTypes = oldTypes
	})
	searchLimit, searchOffset, searchJSON = 10, 0, true
	searchAccount, searchCollection, searchMessageTypes = "", "", nil

	stop := captureStdout(t)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runHTTPSearch(cmd, "subject:Needle")
	output := stop()

	require.NoError(t, err)
	assert.Contains(t, output, `"id": `+formatCount(messageID))
	assert.Contains(t, output, `"subject": "Needle subject"`)
}

func TestShowMessageReadsSQLiteDirectly(t *testing.T) {
	_, messageID := seedDirectCLIArchive(t)
	oldJSON := showMessageJSON
	showMessageJSON = true
	t.Cleanup(func() { showMessageJSON = oldJSON })

	stop := captureStdout(t)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := showHTTPMessage(cmd, formatCount(messageID))
	output := stop()

	require.NoError(t, err)
	assert.Contains(t, output, `"source_message_id": "gmail-needle-1"`)
	assert.Contains(t, output, `"body_text": "needle body"`)
}

func TestStatsReadsSQLiteDirectly(t *testing.T) {
	dataDir, _ := seedDirectCLIArchive(t)
	oldAccount, oldCollection := statsAccount, statsCollection
	statsAccount, statsCollection = "", ""
	t.Cleanup(func() { statsAccount, statsCollection = oldAccount, oldCollection })

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	err := runStats(cmd, nil)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "Database: "+filepath.Join(dataDir, "msgvault.db"))
	assert.Contains(t, out.String(), "Messages:    1")
}
