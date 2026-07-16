package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/rittikbasu/msgvault-lite/internal/gmail"
	"github.com/rittikbasu/msgvault-lite/internal/oauth"
	"github.com/rittikbasu/msgvault-lite/internal/store"
	"github.com/rittikbasu/msgvault-lite/internal/sync"
	"golang.org/x/oauth2"
)

var syncIncrementalCmd = &cobra.Command{
	Use:   "sync [email]",
	Short: "Sync new and changed messages from the configured Gmail account",
	Long: `Perform an incremental synchronization using the Gmail History API.

This is faster than a full sync as it only fetches changes since the last sync.
Requires a prior full sync to establish the history ID baseline.

If no email is specified, syncs the configured Gmail account.

If history is too old (Gmail returns 404), automatically falls back to a full
sync, which is resumable and skips already-archived messages.

Examples:
  msgvault sync
  msgvault sync you@gmail.com`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSyncIncrementalLocal,
}

func runSyncIncrementalLocal(cmd *cobra.Command, args []string) error {
	s, cleanup, err := openWritableStoreAndInit()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nInterrupted. Saving checkpoint...")
		cancel()
	}()

	source, err := singleGmailSyncSource(s, args)
	if err != nil {
		return err
	}
	if !cfg.OAuth.HasAnyConfig() {
		return errors.New("OAuth not configured")
	}
	if err := runIncrementalSync(ctx, s, oauthManagerCache(), source); err != nil {
		return fmt.Errorf("sync %s: %w", source.Identifier, err)
	}

	return nil
}

func singleGmailSyncSource(s *store.Store, args []string) (*store.Source, error) {
	sources, err := s.ListSources("")
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	if len(sources) != 1 {
		return nil, fmt.Errorf("msgvault-lite requires exactly one configured Gmail source; found %d", len(sources))
	}
	source := sources[0]
	if source.SourceType != sourceTypeGmail {
		return nil, fmt.Errorf("configured source type %q is unsupported; msgvault-lite supports Gmail only", source.SourceType)
	}
	if len(args) == 1 && args[0] != source.Identifier && (!source.DisplayName.Valid || args[0] != source.DisplayName.String) {
		return nil, fmt.Errorf("account %q is not configured; this archive belongs to %s", args[0], source.Identifier)
	}
	return source, nil
}

func runIncrementalSync(ctx context.Context, s *store.Store, getOAuthMgr func(string) (*oauth.Manager, error), source *store.Source) error {
	if !source.SyncCursor.Valid || source.SyncCursor.String == "" {
		return errors.New("no history ID - run 'sync-full' first")
	}

	email := source.Identifier
	appName := sourceOAuthApp(source)
	var tokenSource oauth2.TokenSource
	var tsErr error

	if saKeyPath := cfg.OAuth.ServiceAccountKeyFor(appName); saKeyPath != "" {
		saMgr, saErr := oauth.NewServiceAccountManager(saKeyPath)
		if saErr != nil {
			return fmt.Errorf("service account: %w", saErr)
		}
		tokenSource, tsErr = saMgr.TokenSource(ctx, email)
		if tsErr != nil {
			return tsErr
		}
	} else {
		oauthMgr, oaErr := getOAuthMgr(appName)
		if oaErr != nil {
			return oaErr
		}
		interactive := isatty.IsTerminal(os.Stdin.Fd()) ||
			isatty.IsCygwinTerminal(os.Stdin.Fd())
		tokenSource, tsErr = getTokenSourceWithReauth(ctx, oauthMgr, email, interactive, gmailReauthHint)
		if tsErr != nil {
			return tsErr
		}
	}

	// Create Gmail client
	rateLimiter := gmail.NewRateLimiter(float64(cfg.Sync.RateLimitQPS))
	client := gmail.NewClient(tokenSource,
		gmail.WithLogger(logger),
		gmail.WithRateLimiter(rateLimiter),
	)
	defer func() { _ = client.Close() }()

	// Set up sync options
	opts := sync.DefaultOptions()
	opts.AttachmentsDir = cfg.AttachmentsDir()

	// Create syncer with progress reporter
	syncer := sync.New(client, s, opts).
		WithLogger(logger).
		WithProgress(&CLIProgress{})

	// Run incremental sync
	startTime := time.Now()
	fmt.Printf("Starting incremental sync for %s\n", email)
	fmt.Printf("Last history ID: %s\n", source.SyncCursor.String)

	summary, err := syncer.Incremental(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Println("\nSync interrupted. Run again to resume.")
			return syncInterruptionError(ctx)
		}
		// The history baseline is gone (Gmail keeps only ~7 days), so an
		// incremental sync can never succeed again for this account. Fall
		// back to a full sync instead of telling the user to run one:
		// it is resumable and skips already-archived messages.
		if errors.Is(err, sync.ErrHistoryExpired) {
			fmt.Println("History ID has expired (Gmail keeps only ~7 days of history).")
			fmt.Println("Falling back to a full sync; already-archived messages are skipped.")
			fmt.Println()
			return runFullSync(ctx, s, getOAuthMgr, source)
		}
		return fmt.Errorf("sync failed: %w", err)
	}

	// Print summary
	fmt.Println()
	fmt.Println("Sync complete!")
	fmt.Printf("  Duration:      %s\n", summary.Duration.Round(time.Second))
	fmt.Printf("  Changes:       %d processed, %d added\n",
		summary.MessagesFound, summary.MessagesAdded)
	fmt.Printf("  Downloaded:    %.2f MB\n", float64(summary.BytesDownloaded)/(1024*1024))
	if summary.Errors > 0 {
		fmt.Printf("  Errors:        %d\n", summary.Errors)
	}

	elapsed := time.Since(startTime)
	logger.Info("incremental sync completed",
		keyEmail, email,
		"messages_added", summary.MessagesAdded,
		"elapsed", elapsed,
	)

	return nil
}

func syncInterruptionError(ctx context.Context) error {
	return fmt.Errorf("sync interrupted: %w", ctx.Err())
}

func init() {
	rootCmd.AddCommand(syncIncrementalCmd)
}
