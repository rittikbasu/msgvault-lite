package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetainLiteCommandsRemovesLegacySurface(t *testing.T) {
	root := &cobra.Command{Use: "msgvault"}
	for _, name := range []string{"status", "messages", "show", "search", "sync", "backup", "openapi", "deduplicate", "list-accounts", "tui"} {
		root.AddCommand(&cobra.Command{Use: name})
	}

	retainLiteCommands(root)

	names := make([]string, 0, len(root.Commands()))
	for _, command := range root.Commands() {
		names = append(names, command.Name())
	}
	assert.ElementsMatch(t, []string{"backup", "messages", "search", "show", "status", "sync"}, names)
}

func TestLiteCommandCanonicalNamesKeepLegacyAliases(t *testing.T) {
	assert.Equal(t, "status", statsCmd.Name())
	assert.Contains(t, statsCmd.Aliases, "stats")
	assert.Equal(t, "show", showMessageCmd.Name())
	assert.Contains(t, showMessageCmd.Aliases, "show-message")
}

func TestSyncIncrementalAliasIsRejected(t *testing.T) {
	_, _, err := rootCmd.Find([]string{"sync-incremental"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "unknown command")
}
