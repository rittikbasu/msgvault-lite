package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/search"
	"go.kenn.io/msgvault/internal/store"
)

var (
	searchLimit  int
	searchOffset int
	searchJSON   bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search messages using Gmail-like query syntax",
	Long: `Search your local email archive using Gmail-like query syntax.

Supported operators:
  from:        Sender email address
  to:          Recipient email address
  cc:          CC recipient
  bcc:         BCC recipient
  subject:     Subject text search
  label:       Gmail label (or l: shorthand)
  has:         has:attachment - messages with attachments
  before:      Messages before date (YYYY-MM-DD)
  after:       Messages after date (YYYY-MM-DD)
  older_than:  Relative date (7d, 2w, 1m, 1y)
  newer_than:  Relative date
  larger:      Size filter (5M, 100K)
  smaller:     Size filter

Bare words and "quoted phrases" perform full-text search.

Examples:
  msgvault search from:alice@example.com has:attachment
  msgvault search subject:meeting after:2024-01-01
  msgvault search project report newer_than:30d
  msgvault search '"exact phrase"' label:INBOX`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Join all args to form the query (allows unquoted multi-term searches)
		queryStr := strings.Join(args, " ")

		if queryStr == "" {
			return usageErr(cmd, errors.New("provide a search query"))
		}

		if searchLimit <= 0 {
			return usageErr(cmd, fmt.Errorf("--limit must be a positive integer, got %d", searchLimit))
		}
		if searchLimit > maxJSONPageSize {
			return usageErr(cmd, fmt.Errorf("--limit must not exceed %d, got %d", maxJSONPageSize, searchLimit))
		}
		if searchOffset < 0 {
			return usageErr(cmd, fmt.Errorf("--offset must be non-negative, got %d", searchOffset))
		}

		// Reject known operators with invalid values (e.g. before:2025-13-45)
		// rather than silently dropping the filter and running a wider query.
		// Checked before the empty-query test so the user sees the offending
		// value instead of a misleading "empty search query".
		if err := search.Parse(queryStr).Err(); err != nil {
			return usageErr(cmd, err)
		}
		if search.Parse(queryStr).IsEmpty() {
			return errors.New("empty search query")
		}
		return runSearch(cmd, queryStr)
	},
}

func runSearch(cmd *cobra.Command, queryStr string) error {
	s, err := store.OpenReadOnly(cfg.DatabasePath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	parsed := search.Parse(queryStr)
	fetchLimit := searchLimit
	if searchJSON {
		fetchLimit++
	}
	results, err := query.NewSQLiteEngine(s.DB()).Search(
		cmd.Context(), parsed, fetchLimit, searchOffset,
	)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	if searchJSON {
		hasMore := len(results) > searchLimit
		if hasMore {
			results = results[:searchLimit]
		}
		return outputSearchResultsJSON(cmd.OutOrStdout(), results, hasMore)
	}
	if len(results) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No messages found.")
		return nil
	}
	return outputSearchResultsTable(cmd.OutOrStdout(), results)
}

// nil error return mirrors outputSearchResultsJSON so callers can return
// either uniformly; tabwriter output never fails.
//
//nolint:unparam // symmetry with error-returning outputSearchResultsJSON sibling
func outputSearchResultsTable(out io.Writer, results []query.MessageSummary) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tDATE\tFROM\tSUBJECT\tSIZE")
	_, _ = fmt.Fprintln(w, "──\t────\t────\t───────\t────")

	for _, msg := range results {
		date := msg.SentAt.Format("2006-01-02")
		from := truncate(summaryFromDisplay(msg), 30)
		subject := truncate(msg.Subject, 50)
		size := formatSize(msg.SizeEstimate)
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", msg.ID, date, from, subject, size)
	}

	_ = w.Flush()
	_, _ = fmt.Fprintf(out, "\n%s\n", formatShowingResults(len(results)))
	return nil
}

func summaryFromDisplay(msg query.MessageSummary) string {
	for _, value := range []string{msg.FromEmail, msg.FromName, msg.FromPhone} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func outputSearchResultsJSON(out io.Writer, results []query.MessageSummary, hasMore bool) error {
	items := make([]jsonMessageSummary, len(results))
	for i, msg := range results {
		items[i] = queryMessageSummary(msg)
	}
	return writeJSON(out, jsonListResponse{
		SchemaVersion: jsonSchemaVersion,
		Items:         items,
		Page: jsonPage{
			Limit:    searchLimit,
			Offset:   searchOffset,
			Returned: len(items),
			HasMore:  hasMore,
		},
	})
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "n", 50, "Maximum number of results (max 200)")
	searchCmd.Flags().IntVar(&searchOffset, "offset", 0, "Skip first N results")
	searchCmd.Flags().BoolVar(&searchJSON, flagJSON, false, "Output stable JSON schema v1")
}
