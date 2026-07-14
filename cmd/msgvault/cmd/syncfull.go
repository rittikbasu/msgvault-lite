package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/gmail"
	"go.kenn.io/msgvault/internal/oauth"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/sync"
	"golang.org/x/oauth2"
)

var (
	syncQuery    string
	syncNoResume bool
	syncBefore   string
	syncAfter    string
	syncLimit    int
)

var syncFullCmd = &cobra.Command{
	Use:   "sync-full [email]",
	Short: "Perform a full sync of the configured Gmail account",
	Long: `Perform a full synchronization of a Gmail account.

Downloads all messages matching the query (or all messages if no query).
Supports resumption from interruption - just run again to continue.

If no email is specified, syncs the configured Gmail account.

Date filters:
  --after 2024-01-01     Only messages on or after this date
  --before 2024-12-31    Only messages before this date

Examples:
  msgvault sync-full
  msgvault sync-full you@gmail.com
  msgvault sync-full you@gmail.com --after 2024-01-01
  msgvault sync-full you@gmail.com --query "from:someone@example.com"
  msgvault sync-full you@gmail.com --noresume    # Force fresh sync`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateSyncFullFlags(cmd); err != nil {
			return err
		}
		return runSyncFullLocal(cmd, args)
	},
}

func validateSyncFullFlags(cmd *cobra.Command) error {
	if syncLimit < 0 {
		return usageErr(cmd, errors.New("--limit must be a non-negative number"))
	}
	if syncAfter != "" {
		if _, err := time.Parse("2006-01-02", syncAfter); err != nil {
			return usageErr(cmd, fmt.Errorf("invalid --after date %q (expected YYYY-MM-DD): %w", syncAfter, err))
		}
	}
	if syncBefore != "" {
		if _, err := time.Parse("2006-01-02", syncBefore); err != nil {
			return usageErr(cmd, fmt.Errorf("invalid --before date %q (expected YYYY-MM-DD): %w", syncBefore, err))
		}
	}
	return nil
}

func runSyncFullLocal(cmd *cobra.Command, args []string) error {
	s, cleanup, err := openWritableStoreAndInit()
	if err != nil {
		return err
	}
	defer cleanup()

	getOAuthMgr := oauthManagerCache()
	source, err := singleGmailSyncSource(s, args)
	if err != nil {
		return err
	}
	sources := []*store.Source{source}
	var syncErrors []string

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Handle Ctrl+C gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nInterrupted. Saving checkpoint...")
		cancel()
	}()

	// Embedding is no longer driven by sync: newly-ingested messages
	// get embed_gen = NULL by column default and the scan-and-fill
	// embed worker (msgvault embeddings build / the serve daemon)
	// picks them up.

	for _, src := range sources {
		if ctx.Err() != nil {
			break
		}

		// Ensure credentials are available before syncing Gmail sources.
		if src.SourceType == sourceTypeGmail || src.SourceType == "" {
			appName := sourceOAuthApp(src)
			if cfg.OAuth.ServiceAccountKeyFor(appName) == "" {
				if _, err := getOAuthMgr(appName); err != nil {
					syncErrors = append(syncErrors, fmt.Sprintf("%s: %v", src.Identifier, err))
					continue
				}
			}
		}

		if err := runFullSync(ctx, s, getOAuthMgr, src); err != nil {
			syncErrors = append(syncErrors, fmt.Sprintf("%s: %v", src.Identifier, err))
			continue
		}
	}

	if len(syncErrors) > 0 {
		fmt.Println()
		fmt.Println("Errors:")
		for _, e := range syncErrors {
			fmt.Printf("  %s\n", e)
		}
		return fmt.Errorf("%d account(s) failed to sync: %s",
			len(syncErrors), strings.Join(syncErrors, "; "))
	}

	return nil
}

// buildAPIClient creates the appropriate gmail.API client for the given
// source. Browser OAuth and service-account flows are both constrained to the
// exact Gmail readonly scope.
func buildAPIClient(ctx context.Context, src *store.Source, getOAuthMgr func(string) (*oauth.Manager, error)) (gmail.API, error) {
	if src.SourceType != sourceTypeGmail {
		return nil, fmt.Errorf("unsupported source type %q; msgvault-lite supports Gmail only", src.SourceType)
	}
	appName := sourceOAuthApp(src)
	var tokenSource oauth2.TokenSource

	if saKeyPath := cfg.OAuth.ServiceAccountKeyFor(appName); saKeyPath != "" {
		saMgr, err := oauth.NewServiceAccountManager(saKeyPath)
		if err != nil {
			return nil, fmt.Errorf("service account: %w", err)
		}
		tokenSource, err = saMgr.TokenSource(ctx, src.Identifier)
		if err != nil {
			return nil, err
		}
	} else {
		oauthMgr, err := getOAuthMgr(appName)
		if err != nil {
			return nil, err
		}
		interactive := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
		tokenSource, err = getTokenSourceWithReauth(ctx, oauthMgr, src.Identifier, interactive, gmailReauthHint)
		if err != nil {
			return nil, err
		}
	}

	rateLimiter := gmail.NewRateLimiter(float64(cfg.Sync.RateLimitQPS))
	return gmail.NewClient(tokenSource,
		gmail.WithLogger(logger),
		gmail.WithRateLimiter(rateLimiter),
	), nil
}

func runFullSync(ctx context.Context, s *store.Store, getOAuthMgr func(string) (*oauth.Manager, error), src *store.Source) error {
	progress := &CLIProgress{}
	apiClient, err := buildAPIClient(ctx, src, getOAuthMgr)
	if err != nil {
		return err
	}
	defer func() { _ = apiClient.Close() }()

	query := buildSyncQuery()

	// Set up sync options
	opts := sync.DefaultOptions()
	opts.SourceType = src.SourceType
	opts.Query = query
	opts.NoResume = syncNoResume
	opts.Limit = syncLimit
	opts.AttachmentsDir = cfg.AttachmentsDir()

	// Create syncer with progress reporter
	syncer := sync.New(apiClient, s, opts).
		WithLogger(logger).
		WithProgress(progress)

	// Run sync
	startTime := time.Now()
	displayID := src.Identifier
	if src.DisplayName.Valid && src.DisplayName.String != "" {
		displayID = src.DisplayName.String
	}
	fmt.Printf("Starting full sync for %s\n", displayID)
	if query != "" {
		fmt.Printf("Query: %s\n", query)
	}
	fmt.Println()

	summary, err := syncer.Full(ctx, src.Identifier)
	if err != nil {
		if ctx.Err() != nil {
			if opts.NoResume {
				fmt.Println("\nSync interrupted. Run again to restart (already-imported messages will be skipped).")
			} else {
				fmt.Println("\nSync interrupted. Run again to resume.")
			}
			return nil
		}
		return fmt.Errorf("sync failed: %w", err)
	}

	// Print summary; skip the spacer when no progress lines were
	// printed so a no-op sync doesn't emit stacked blank lines.
	if progress.printedAnything() {
		fmt.Println()
	}
	fmt.Println("Sync complete!")
	fmt.Printf("  Duration:      %s\n", summary.Duration.Round(time.Second))
	fmt.Printf("  Messages:      %d found, %d added, %d skipped\n",
		summary.MessagesFound, summary.MessagesAdded, summary.MessagesSkipped)
	fmt.Printf("  Downloaded:    %.2f MB\n", float64(summary.BytesDownloaded)/(1024*1024))
	if summary.Errors > 0 {
		fmt.Printf("  Errors:        %d\n", summary.Errors)
	}
	if summary.WasResumed {
		fmt.Printf("  (Resumed from checkpoint)\n")
	}

	// Print timing stats
	if summary.MessagesAdded > 0 {
		messagesPerSec := float64(summary.MessagesAdded) / summary.Duration.Seconds()
		fmt.Printf("  Rate:          %.1f messages/sec\n", messagesPerSec)
	}

	elapsed := time.Since(startTime)
	logger.Info("sync completed",
		"identifier", displayID,
		"messages_added", summary.MessagesAdded,
		"elapsed", elapsed,
	)

	return nil
}

// buildSyncQuery constructs a Gmail search query from flags.
func buildSyncQuery() string {
	parts := []string{}

	if syncAfter != "" {
		parts = append(parts, "after:"+syncAfter)
	}
	if syncBefore != "" {
		parts = append(parts, "before:"+syncBefore)
	}
	if syncQuery != "" {
		parts = append(parts, syncQuery)
	}

	result := ""
	var resultSb447 strings.Builder
	for i, p := range parts {
		if i > 0 {
			resultSb447.WriteString(" ")
		}
		resultSb447.WriteString(p)
	}
	result += resultSb447.String()
	return result
}

// CLIProgress implements gmail.SyncProgressWithDate for terminal output.
// progressOutputMode selects how CLIProgress renders updates.
type progressOutputMode int

const (
	// progressModeAuto detects the mode from stdout on first use.
	progressModeAuto progressOutputMode = iota
	// progressModeTTY redraws a single status line in place with \r.
	progressModeTTY
	// progressModePlain emits one newline-terminated update at a lower
	// cadence. Used when stdout is a pipe — the daemon CLI subprocess,
	// redirected output, CI — where \r overwriting cannot work and would
	// interleave with stderr into one unreadable blob.
	progressModePlain
)

const (
	cliProgressTTYInterval   = 2 * time.Second
	cliProgressPlainInterval = 30 * time.Second
	// Folder listing is much faster per item than message fetching, so
	// plain-mode listing updates can come more often without flooding.
	cliListPlainInterval = 15 * time.Second
)

type CLIProgress struct {
	startTime  time.Time
	lastPrint  time.Time
	latestDate time.Time
	// Cache latest stats for combined display
	processed int64
	added     int64
	skipped   int64
	mode      progressOutputMode
	out       io.Writer // defaults to os.Stdout; tests inject a buffer

	printedProgress bool      // a sync progress line has been printed
	printedList     bool      // a folder-listing line has been printed
	lastListPrint   time.Time // throttle for intermediate listing updates
}

// printedAnything reports whether any progress output was emitted,
// so callers can avoid stacking blank lines around silent syncs.
func (p *CLIProgress) printedAnything() bool {
	return p.printedProgress || p.printedList
}

func (p *CLIProgress) OnStart(total int64) {
	now := time.Now()
	p.startTime = now
	p.lastPrint = now
	// Don't print Gmail's estimate - it's often wildly inaccurate
}

func (p *CLIProgress) OnProgress(processed, added, skipped int64) {
	if p.startTime.IsZero() {
		now := time.Now()
		p.startTime = now
		p.lastPrint = now
	}
	p.processed = processed
	p.added = added
	p.skipped = skipped
	p.printProgress()
}

func (p *CLIProgress) OnLatestDate(date time.Time) {
	if p.startTime.IsZero() {
		now := time.Now()
		p.startTime = now
		p.lastPrint = now
	}
	// Record only; the next OnProgress renders it. Printing here would
	// consume the throttle window with whatever counters happen to be
	// cached — in plain mode that emits a permanent line with stale (or
	// zero) Scanned/Added values and suppresses the accurate one that
	// follows.
	p.latestDate = date
}

// OnIMAPListProgress renders mailbox-enumeration progress for IMAP
// syncs (the phase before any message is fetched, which is otherwise
// silent). The first and final updates always print; the final one is
// a permanent summary line so even an instant all-skipped resync shows
// what happened.
func (p *CLIProgress) OnIMAPListProgress(done, total int, mailbox string, found, unchanged int) {
	tty := p.outputMode() == progressModeTTY

	if done >= total {
		prefix := ""
		if tty && p.printedList {
			prefix = "\r" // overwrite the in-place listing line
		}
		skipNote := ""
		if unchanged > 0 {
			skipNote = fmt.Sprintf(", %d unchanged (skipped)", unchanged)
		}
		// Trailing spaces overwrite leftovers of a longer in-place line.
		_, _ = fmt.Fprintf(p.writer(),
			"%s  Checked %d folders: %d messages to examine%s                    \n",
			prefix, total, found, skipNote)
		p.printedList = true
		return
	}

	interval := cliProgressTTYInterval
	if !tty {
		interval = cliListPlainInterval
	}
	if p.printedList && time.Since(p.lastListPrint) < interval {
		return
	}
	p.printedList = true
	p.lastListPrint = time.Now()

	if tty {
		_, _ = fmt.Fprintf(p.writer(),
			"\r  Checking folders: %d/%d (%s)    ", done, total, mailbox)
		return
	}
	if done == 0 {
		_, _ = fmt.Fprintf(p.writer(), "  Checking %d folders...\n", total)
		return
	}
	_, _ = fmt.Fprintf(p.writer(), "  Checking folders: %d/%d\n", done, total)
}

func (p *CLIProgress) outputMode() progressOutputMode {
	if p.mode == progressModeAuto {
		if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
			p.mode = progressModeTTY
		} else {
			p.mode = progressModePlain
		}
	}
	return p.mode
}

func (p *CLIProgress) writer() io.Writer {
	if p.out == nil {
		return os.Stdout
	}
	return p.out
}

func (p *CLIProgress) printProgress() {
	// Throttle: an in-place line can refresh every 2 seconds, but each
	// plain-mode update is a permanent line, so those come every 30.
	// The first line always prints so a slow fetch (IMAP especially)
	// shows signs of life as soon as the first page completes.
	interval := cliProgressTTYInterval
	if p.outputMode() == progressModePlain {
		interval = cliProgressPlainInterval
	}
	if p.printedProgress && time.Since(p.lastPrint) < interval {
		return
	}
	p.printedProgress = true
	p.lastPrint = time.Now()

	elapsed := time.Since(p.startTime)
	rate := 0.0
	if elapsed.Seconds() >= 1 {
		rate = float64(p.added) / elapsed.Seconds()
	}

	// Format elapsed time nicely
	elapsedStr := formatCLIProgressDuration(elapsed, cliProgressDurationSpaced)

	// Format latest message date if available
	dateStr := ""
	if !p.latestDate.IsZero() {
		dateStr = " | Latest: " + p.latestDate.Format("Jan 2006")
	}

	if p.outputMode() == progressModePlain {
		_, _ = fmt.Fprintf(p.writer(),
			"  Scanned: %d | Added: %d | Skipped: %d | Rate: %.1f/s | Elapsed: %s%s\n",
			p.processed, p.added, p.skipped, rate, elapsedStr, dateStr)
		return
	}
	_, _ = fmt.Fprintf(p.writer(),
		"\r  Scanned: %d | Added: %d | Skipped: %d | Rate: %.1f/s | Elapsed: %s%s    ",
		p.processed, p.added, p.skipped, rate, elapsedStr, dateStr)
}

func (p *CLIProgress) OnComplete(summary *gmail.SyncSummary) {
	if p.outputMode() == progressModePlain {
		return // every plain-mode update already ended its line
	}
	_, _ = fmt.Fprintln(p.writer()) // terminate the in-place progress line
}

func (p *CLIProgress) OnError(err error) {
	_, _ = fmt.Fprintf(p.writer(), "\nError: %v\n", err)
}

func init() {
	syncFullCmd.Flags().StringVar(&syncQuery, "query", "", "Gmail search query")
	syncFullCmd.Flags().BoolVar(&syncNoResume, "noresume", false, "Force fresh sync (don't resume)")
	syncFullCmd.Flags().StringVar(&syncBefore, "before", "", "Only messages before this date (YYYY-MM-DD)")
	syncFullCmd.Flags().StringVar(&syncAfter, "after", "", "Only messages after this date (YYYY-MM-DD)")
	syncFullCmd.Flags().IntVar(&syncLimit, "limit", 0, "Limit number of messages (for testing)")
	rootCmd.AddCommand(syncFullCmd)
}
