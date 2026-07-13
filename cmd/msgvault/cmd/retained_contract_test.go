package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestResolveMessageIDArgValidation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		err   string
	}{
		{name: "numeric", input: " 42 ", want: "42"},
		{name: "Gmail ID", input: "18f0abc123def", want: "18f0abc123def"},
		{name: "blank", input: "  ", err: "invalid message ID"},
		{name: "fraction", input: "42.5", err: "expected a whole number"},
		{name: "exponent", input: "1e3", err: "expected a whole number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMessageIDArg(tt.input)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSearchValidatesBeforeOpeningArchive(t *testing.T) {
	oldLimit, oldOffset, oldMode := searchLimit, searchOffset, searchMode
	oldAccount, oldCollection := searchAccount, searchCollection
	oldTypes := append([]string(nil), searchMessageTypes...)
	t.Cleanup(func() {
		searchLimit, searchOffset, searchMode = oldLimit, oldOffset, oldMode
		searchAccount, searchCollection = oldAccount, oldCollection
		searchMessageTypes = oldTypes
	})

	tests := []struct {
		name    string
		args    []string
		setup   func()
		wantErr string
	}{
		{name: "empty", wantErr: "provide a search query"},
		{name: "mode", args: []string{"needle"}, setup: func() { searchMode = "hybrid" }, wantErr: "only fts is supported"},
		{name: "message type", args: []string{"needle"}, setup: func() { searchMessageTypes = []string{"carrier_pigeon"} }, wantErr: "invalid --message-type"},
		{name: "limit", args: []string{"needle"}, setup: func() { searchLimit = 0 }, wantErr: "--limit must be a positive integer"},
		{name: "offset", args: []string{"needle"}, setup: func() { searchOffset = -1 }, wantErr: "--offset must be non-negative"},
		{name: "date", args: []string{"before:2025-13-45"}, wantErr: "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searchLimit, searchOffset, searchMode = 10, 0, "fts"
			searchAccount, searchCollection, searchMessageTypes = "", "", nil
			if tt.setup != nil {
				tt.setup()
			}
			cmd := &cobra.Command{}
			err := searchCmd.RunE(cmd, tt.args)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPrintStatsIncludesRetainedCounts(t *testing.T) {
	var out bytes.Buffer
	printStats(&out, &store.Stats{
		MessageCount:       3,
		SourceDeletedCount: 2,
		ThreadCount:        2,
		AttachmentCount:    1,
		LabelCount:         4,
		SourceCount:        1,
	})

	assert.Contains(t, out.String(), "Messages:    5 (3 active, 2 deleted from source)")
	assert.Contains(t, out.String(), "Threads:     2")
	assert.Contains(t, out.String(), "Attachments: 1")
	assert.Contains(t, out.String(), "Accounts:    1")
}
