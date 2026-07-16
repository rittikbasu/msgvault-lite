package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/rittikbasu/msgvault-lite/internal/store"
)

var statsJSON bool

var statsCmd = &cobra.Command{
	Use:   "status",
	Short: "Show local archive status",
	Long:  `Show statistics about the local email archive.`,
	Args:  cobra.NoArgs,
	RunE:  runStats,
}

func runStats(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	dbPath := cfg.DatabasePath()
	s, err := store.OpenReadOnly(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	dbStats, err := s.GetStatsContext(cmd.Context())
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
	if statsJSON {
		return writeJSON(out, jsonStatusResponse{
			SchemaVersion: jsonSchemaVersion,
			Database:      dbPath,
			Messages: jsonMessageCounts{
				Total:             dbStats.MessageCount + dbStats.SourceDeletedCount,
				Active:            dbStats.MessageCount,
				DeletedFromSource: dbStats.SourceDeletedCount,
			},
			Threads:       dbStats.ThreadCount,
			Attachments:   dbStats.AttachmentCount,
			Labels:        dbStats.LabelCount,
			Accounts:      dbStats.SourceCount,
			DatabaseBytes: dbStats.DatabaseSize,
		})
	}

	_, _ = fmt.Fprintf(out, "Database: %s\n", dbPath)

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
	statsCmd.Flags().BoolVar(&statsJSON, flagJSON, false, "Output stable JSON schema v1")
}
