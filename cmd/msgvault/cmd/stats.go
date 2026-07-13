package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/store"
)

var statsCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"stats"},
	Short:   "Show local archive status",
	Long:    `Show statistics about the local email archive.`,
	Args:    cobra.NoArgs,
	RunE:    runStats,
}

func runStats(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	s, err := store.OpenReadOnly(cfg.DatabaseDSN())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	dbStats, err := s.GetStats()
	if err != nil {
		logger.Warn("stats failed", "error", err.Error())
		return fmt.Errorf("get stats: %w", err)
	}
	logger.Info("stats",
		tableMessages, dbStats.MessageCount,
		"threads", dbStats.ThreadCount,
		tableAttachments, dbStats.AttachmentCount,
		tableLabels, dbStats.LabelCount,
		"accounts", dbStats.SourceCount,
		"db_bytes", dbStats.DatabaseSize,
	)

	_, _ = fmt.Fprintf(out, "Database: %s\n", cfg.DatabaseDSN())

	printStats(out, dbStats)
	return nil
}

func printStats(w io.Writer, s *store.Stats) {
	if s.SourceDeletedCount > 0 {
		total := s.MessageCount + s.SourceDeletedCount
		_, _ = fmt.Fprintf(w, "  Messages:    %s (%s active, %s deleted from source)\n",
			formatCount(total), formatCount(s.MessageCount), formatCount(s.SourceDeletedCount))
	} else {
		_, _ = fmt.Fprintf(w, "  Messages:    %s\n", formatCount(s.MessageCount))
	}
	_, _ = fmt.Fprintf(w, "  Threads:     %s\n", formatCount(s.ThreadCount))
	_, _ = fmt.Fprintf(w, "  Attachments: %s\n", formatCount(s.AttachmentCount))
	_, _ = fmt.Fprintf(w, "  Labels:      %s\n", formatCount(s.LabelCount))
	_, _ = fmt.Fprintf(w, "  Accounts:    %s\n", formatCount(s.SourceCount))
	_, _ = fmt.Fprintf(w, "  Size:        %.2f MB\n", float64(s.DatabaseSize)/(1024*1024))
}

func init() {
	rootCmd.AddCommand(statsCmd)
}
