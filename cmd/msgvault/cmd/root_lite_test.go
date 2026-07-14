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

func TestLiteCommandCanonicalNamesRejectLegacyAliases(t *testing.T) {
	assert.Equal(t, "status", statsCmd.Name())
	assert.Empty(t, statsCmd.Aliases)
	assert.Equal(t, "show", showMessageCmd.Name())
	assert.Empty(t, showMessageCmd.Aliases)

	for _, alias := range []string{"stats", "show-message"} {
		_, _, err := rootCmd.Find([]string{alias})
		require.Error(t, err)
		assert.ErrorContains(t, err, "unknown command")
	}
}

func TestSyncIncrementalAliasIsRejected(t *testing.T) {
	_, _, err := rootCmd.Find([]string{"sync-incremental"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "unknown command")
}
