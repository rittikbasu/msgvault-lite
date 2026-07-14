package cmd

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

var (
	showMessageJSON         bool
	showMessageMaxBodyBytes int
)

var showMessageCmd = &cobra.Command{
	Use:     "show <id>",
	Aliases: []string{"show-message"},
	Short:   "Show full message details",
	Long: `Show the complete details of a local message by its internal ID or Gmail ID.

This command displays the full message including headers, body, labels,
and attachment information. Use --json for programmatic output.

Examples:
  msgvault show 12345
	msgvault show 18f0abc123def --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if showMessageMaxBodyBytes <= 0 {
			return usageErr(cmd, fmt.Errorf("--max-body-bytes must be a positive integer, got %d", showMessageMaxBodyBytes))
		}
		if showMessageMaxBodyBytes > maxJSONBodyBytes {
			return usageErr(cmd, fmt.Errorf("--max-body-bytes must not exceed %d, got %d", maxJSONBodyBytes, showMessageMaxBodyBytes))
		}
		id, err := resolveMessageIDArg(args[0])
		if err != nil {
			return err
		}
		return runShowMessage(cmd, id)
	},
}

// resolveMessageIDArg validates a positional message-reference argument for
// commands that accept either an internal numeric ID or a source/Gmail message
// ID. Empty or whitespace-only input, and malformed numeric input such as
// "42.5" or "1e3", are rejected up front with a clear error so the user is not
// misled by a downstream "message not found". Any other non-empty value is
// forwarded unchanged (it may be a Gmail/source ID like "18f0abc123def").
func resolveMessageIDArg(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("invalid message ID: %q (expected a number or Gmail ID)", raw)
	}
	if _, intErr := strconv.ParseInt(trimmed, 10, 64); intErr != nil {
		if _, floatErr := strconv.ParseFloat(trimmed, 64); floatErr == nil {
			return "", fmt.Errorf("invalid message ID: %q (expected a whole number)", trimmed)
		}
	}
	return trimmed, nil
}

func runShowMessage(cmd *cobra.Command, idStr string) error {
	s, err := store.OpenReadOnly(cfg.DatabasePath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	engine := query.NewSQLiteEngine(s.DB())
	var msg *query.MessageDetail
	if id, parseErr := strconv.ParseInt(idStr, 10, 64); parseErr == nil {
		msg, err = engine.GetMessage(cmd.Context(), id)
	} else {
		msg, err = engine.GetMessageBySourceID(cmd.Context(), idStr)
	}
	if errors.Is(err, store.ErrMessageNotFound) {
		return fmt.Errorf("message not found: %s", idStr)
	}
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}
	if msg == nil {
		return fmt.Errorf("message not found: %s", idStr)
	}

	if showMessageJSON {
		return outputMessageJSON(cmd.OutOrStdout(), msg, showMessageMaxBodyBytes)
	}
	return outputMessageText(cmd.OutOrStdout(), msg)
}

// nil error return mirrors outputMessageJSON so callers can return either
// uniformly; text printing never fails.
//
//nolint:unparam // symmetry with error-returning outputMessageJSON sibling
func outputMessageText(out io.Writer, msg *query.MessageDetail) error {
	// Header section
	_, _ = fmt.Fprintln(out, "═══════════════════════════════════════════════════════════════════════════════")
	_, _ = fmt.Fprintf(out, "Message ID: %d (Gmail: %s)\n", msg.ID, msg.SourceMessageID)
	_, _ = fmt.Fprintln(out, "───────────────────────────────────────────────────────────────────────────────")

	// From
	if len(msg.From) > 0 {
		_, _ = fmt.Fprintf(out, "From:    %s\n", formatAddresses(msg.From))
	}

	// To
	if len(msg.To) > 0 {
		_, _ = fmt.Fprintf(out, "To:      %s\n", formatAddresses(msg.To))
	}

	// CC
	if len(msg.Cc) > 0 {
		_, _ = fmt.Fprintf(out, "Cc:      %s\n", formatAddresses(msg.Cc))
	}

	// BCC
	if len(msg.Bcc) > 0 {
		_, _ = fmt.Fprintf(out, "Bcc:     %s\n", formatAddresses(msg.Bcc))
	}

	// Subject
	_, _ = fmt.Fprintf(out, "Subject: %s\n", msg.Subject)

	// Date
	_, _ = fmt.Fprintf(out, "Date:    %s\n", msg.SentAt.Format(time.RFC1123))

	// Size
	_, _ = fmt.Fprintf(out, "Size:    %s\n", formatSize(msg.SizeEstimate))

	// Labels
	if len(msg.Labels) > 0 {
		_, _ = fmt.Fprintf(out, "Labels:  %s\n", strings.Join(msg.Labels, ", "))
	}

	// Attachments
	if len(msg.Attachments) > 0 {
		_, _ = fmt.Fprintln(out, "\nAttachments:")
		for _, att := range msg.Attachments {
			if att.URL != "" {
				_, _ = fmt.Fprintf(out, "  • %s (%s, link) %s\n", att.Filename, att.MimeType, att.URL)
			} else {
				_, _ = fmt.Fprintf(out, "  • %s (%s, %s)\n", att.Filename, att.MimeType, formatSize(att.Size))
			}
		}
	}

	// Body
	_, _ = fmt.Fprintln(out, "\n═══════════════════════════════════════════════════════════════════════════════")
	if msg.BodyText != "" {
		_, _ = fmt.Fprintln(out, msg.BodyText)
	} else if msg.Snippet != "" {
		_, _ = fmt.Fprintf(out, "[No body text available. Snippet: %s]\n", msg.Snippet)
	} else {
		_, _ = fmt.Fprintln(out, "[No body content available]")
	}
	_, _ = fmt.Fprintln(out, "═══════════════════════════════════════════════════════════════════════════════")

	return nil
}

func outputMessageJSON(out io.Writer, msg *query.MessageDetail, maxBodyBytes int) error {
	return writeJSON(out, jsonShowResponse{
		SchemaVersion: jsonSchemaVersion,
		Message:       jsonMessageDetailFrom(msg, maxBodyBytes),
	})
}

func formatAddresses(addrs []query.Address) string {
	parts := make([]string, len(addrs))
	for i, addr := range addrs {
		if addr.Name != "" {
			parts[i] = fmt.Sprintf("%s <%s>", addr.Name, addr.Email)
		} else {
			parts[i] = addr.Email
		}
	}
	return strings.Join(parts, ", ")
}

func init() {
	rootCmd.AddCommand(showMessageCmd)
	showMessageCmd.Flags().BoolVar(&showMessageJSON, flagJSON, false, "Output stable JSON schema v1")
	showMessageCmd.Flags().IntVar(
		&showMessageMaxBodyBytes,
		"max-body-bytes",
		defaultJSONBodyBytes,
		"Maximum bytes per body field in JSON output (max 1048576)",
	)
}
