package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/store"
)

var (
	messagesLimit  int
	messagesOffset int
	messagesJSON   bool
)

var messagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "List recent archived messages",
	Long:  "List recent messages from the local Gmail archive, newest first.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if messagesLimit <= 0 {
			return usageErr(cmd, fmt.Errorf("--limit must be a positive integer, got %d", messagesLimit))
		}
		if messagesLimit > maxJSONPageSize {
			return usageErr(cmd, fmt.Errorf("--limit must not exceed %d, got %d", maxJSONPageSize, messagesLimit))
		}
		if messagesOffset < 0 {
			return usageErr(cmd, fmt.Errorf("--offset must be non-negative, got %d", messagesOffset))
		}
		return runMessages(cmd)
	},
}

func runMessages(cmd *cobra.Command) error {
	s, err := store.OpenReadOnly(cfg.DatabaseDSN())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	messages, total, err := s.ListMessagesContext(cmd.Context(), messagesOffset, messagesLimit)
	if err != nil {
		return fmt.Errorf("list messages: %w", err)
	}
	if messagesJSON {
		items := make([]jsonMessageSummary, len(messages))
		for i, message := range messages {
			items[i] = apiMessageSummary(message)
		}
		return writeJSON(cmd.OutOrStdout(), jsonListResponse{
			SchemaVersion: jsonSchemaVersion,
			Items:         items,
			Page: jsonPage{
				Limit:    messagesLimit,
				Offset:   messagesOffset,
				Returned: len(items),
				Total:    &total,
				HasMore:  int64(messagesOffset+len(items)) < total,
			},
		})
	}
	if len(messages) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No messages found.")
		return nil
	}

	outputMessagesTable(cmd.OutOrStdout(), messages, total)
	return nil
}

func outputMessagesTable(out io.Writer, messages []store.APIMessage, total int64) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tDATE\tFROM\tSUBJECT\tSIZE")
	_, _ = fmt.Fprintln(w, "──\t────\t────\t───────\t────")
	for _, msg := range messages {
		date := ""
		if !msg.SentAt.IsZero() {
			date = msg.SentAt.Format("2006-01-02")
		}
		from := strings.TrimSpace(msg.From)
		if from == "" {
			from = strings.TrimSpace(msg.FromEmail)
		}
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
			msg.ID,
			date,
			truncate(from, 30),
			truncate(msg.Subject, 50),
			formatSize(msg.SizeEstimate),
		)
	}
	_ = w.Flush()
	_, _ = fmt.Fprintf(out, "\nShowing %s of %s messages.\n", formatCount(int64(len(messages))), formatCount(total))
}

func init() {
	rootCmd.AddCommand(messagesCmd)
	messagesCmd.Flags().IntVarP(&messagesLimit, "limit", "n", 50, "Maximum number of messages (max 200)")
	messagesCmd.Flags().IntVar(&messagesOffset, "offset", 0, "Skip first N messages")
	messagesCmd.Flags().BoolVar(&messagesJSON, flagJSON, false, "Output stable JSON schema v1")
}
