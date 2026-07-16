package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.kenn.io/kit/backup"
	"github.com/rittikbasu/msgvault-lite/internal/backupapp"
)

var (
	backupInitRepo   string
	backupCreateRepo string
	backupListRepo   string
	backupVerifyRepo string

	backupCreateIncludeConfig         bool
	backupCreateIncludeTokens         bool
	backupCreateAllowPlaintextSecrets bool
	backupCreateTag                   string
	backupCreateForceUnlock           bool
	backupCreateJobs                  int

	backupVerifyAll         bool
	backupVerifyQuick       bool
	backupVerifyForceUnlock bool
	backupVerifyJobs        int

	backupRestoreRepo        string
	backupRestoreTarget      string
	backupRestoreOverwrite   bool
	backupRestoreForceUnlock bool
	backupRestoreJobs        int

	// backupCreateProgress selects backup create's progress rendering mode:
	// auto (default), bar, or plain. It is hidden/undocumented — see
	// resolveClientBackupProgressFlag in backup_progress.go for why it exists
	// at all (the daemon-proxied subprocess can't detect the real terminal).
	backupCreateProgress string
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Back up the archive to a snapshot repository",
}

var backupInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new backup repository",
	Args:  cobra.NoArgs,
	RunE:  runBackupInit,
}

var backupCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a backup snapshot",
	Args:  cobra.NoArgs,
	RunE:  runBackupCreate,
}

var backupListCmd = &cobra.Command{
	Use:   cmdUseList,
	Short: "List backup snapshots",
	Args:  cobra.NoArgs,
	RunE:  runBackupList,
}

var backupVerifyCmd = &cobra.Command{
	Use:   "verify [SNAPSHOT]",
	Short: "Verify backup repository integrity",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupVerify,
}

var backupRestoreCmd = &cobra.Command{
	Use:   "restore [SNAPSHOT]",
	Short: "Restore a snapshot into a target directory and prove the result",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupRestore,
}

// resolveBackupRepo applies the standard --repo precedence for every backup
// subcommand: an explicit flag wins, else the configured [backup] repo,
// else an error naming both ways to set it.
func resolveBackupRepo(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if cfg != nil && cfg.Backup.Repo != "" {
		return cfg.Backup.Repo, nil
	}
	return "", errors.New("backup: no repository configured; pass --repo or set [backup] repo in config.toml")
}

func runBackupInit(cmd *cobra.Command, _ []string) error {
	repo, err := resolveBackupRepo(backupInitRepo)
	if err != nil {
		return err
	}
	r, err := backup.Init(repo)
	if err != nil {
		return fmt.Errorf("initializing backup repository: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Initialized backup repository %s at %s\n",
		r.Config().RepoID, r.Root()); err != nil {
		return fmt.Errorf("write backup init output: %w", err)
	}
	return nil
}

func runBackupList(cmd *cobra.Command, _ []string) error {
	repo, err := resolveBackupRepo(backupListRepo)
	if err != nil {
		return err
	}
	r, err := backup.Open(repo)
	if err != nil {
		return fmt.Errorf("opening backup repository: %w", err)
	}
	snapshots, err := r.ListSnapshots()
	if err != nil {
		return fmt.Errorf("listing snapshots: %w", err)
	}
	return printBackupSnapshots(cmd.OutOrStdout(), snapshots)
}

func printBackupSnapshots(w io.Writer, snapshots []*backup.Manifest) error {
	if len(snapshots) == 0 {
		if _, err := fmt.Fprintln(w, "No snapshots found."); err != nil {
			return fmt.Errorf("write backup list output: %w", err)
		}
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SNAPSHOT\tCREATED\tMESSAGES\tBYTES ADDED\tTAG")
	for _, m := range snapshots {
		tag := m.Options.Tag
		if tag == "" {
			tag = "-"
		}
		st, err := backupapp.ParseStats(m.Stats)
		if err != nil {
			return fmt.Errorf("snapshot %s: parsing manifest stats: %w", m.SnapshotID, err)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			m.SnapshotID, m.CreatedAt, formatCount(st.Messages), formatSize(m.BytesAdded), tag)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write backup list output: %w", err)
	}
	return nil
}

func runBackupVerify(cmd *cobra.Command, args []string) error {
	repo, err := resolveBackupRepo(backupVerifyRepo)
	if err != nil {
		return err
	}
	r, err := backup.Open(repo)
	if err != nil {
		return fmt.Errorf("opening backup repository: %w", err)
	}
	var snapshotID string
	if len(args) > 0 {
		snapshotID = args[0]
	}
	// backup verify never proxies through the daemon (cliRunCommandAllowed
	// only admits "backup create"), so cmd.OutOrStdout() here is always the
	// real end-user process's own stdout: auto-detection is safe without a
	// --progress flag to route it through a subprocess boundary.
	renderer := newBackupProgressRenderer(cmd.OutOrStdout(), progressModeAuto)
	// An error mid-stage leaves the in-place TTY line open; close it so the
	// error prints on its own row.
	defer renderer.finish()
	result, err := backup.Verify(cmd.Context(), r, backupapp.New(Version), backup.VerifyOptions{
		SnapshotID:  snapshotID,
		All:         backupVerifyAll,
		Quick:       backupVerifyQuick,
		ForceUnlock: backupVerifyForceUnlock,
		Jobs:        backupVerifyJobs,
		Progress:    renderer.handle,
	})
	if err != nil {
		return fmt.Errorf("verifying backup repository: %w", err)
	}
	for _, p := range result.Problems {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "problem: snapshot %s: %s\n", p.SnapshotID, p.Detail)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "verified %d snapshots, %d blobs; %d problems\n",
		len(result.Snapshots), result.BlobsChecked, len(result.Problems))
	if len(result.Problems) > 0 {
		return fmt.Errorf("backup verify: found %d problem(s)", len(result.Problems))
	}
	return nil
}

// runBackupRestore materializes a snapshot into --target and proves the
// result. Like verify, it never proxies through the daemon: it reads only
// the repository and writes only the target, never the live archive.
func runBackupRestore(cmd *cobra.Command, args []string) error {
	repo, err := resolveBackupRepo(backupRestoreRepo)
	if err != nil {
		return err
	}
	r, err := backup.Open(repo)
	if err != nil {
		return fmt.Errorf("opening backup repository: %w", err)
	}
	releaseTarget, err := acquireRestoreTargetLock(backupRestoreTarget)
	if err != nil {
		return err
	}
	defer releaseTarget()
	var snapshotID string
	if len(args) > 0 {
		snapshotID = args[0]
	}
	renderer := newBackupProgressRenderer(cmd.OutOrStdout(), progressModeAuto)
	defer renderer.finish()
	res, err := backup.Restore(cmd.Context(), r, backupapp.New(Version), backup.RestoreOptions{
		SnapshotID:  snapshotID,
		TargetDir:   backupRestoreTarget,
		Overwrite:   backupRestoreOverwrite,
		Jobs:        backupRestoreJobs,
		ForceUnlock: backupRestoreForceUnlock,
		Progress:    renderer.handle,
	})
	if err != nil {
		return fmt.Errorf("restoring snapshot: %w", err)
	}
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "Restored snapshot %s to %s\n", res.SnapshotID, backupRestoreTarget)
	_, _ = fmt.Fprintf(out, "Database: %s (%s)\n", res.DBPath, formatSize(res.DBBytes))
	_, _ = fmt.Fprintf(out, "Attachments: %d (%s)\n", res.AttachmentBlobs, formatSize(res.AttachmentBytes))
	if res.ExtrasFiles > 0 {
		_, _ = fmt.Fprintf(out, "Extras files: %d\n", res.ExtrasFiles)
	}
	_, _ = fmt.Fprintf(out, "Proof: integrity_check ok, manifest stats match\n")
	_, _ = fmt.Fprintf(out, "Duration: %.1fs\n", res.Duration.Seconds())
	return nil
}

// acquireRestoreTargetLock serializes a restore into the configured archive
// with sync, imports, repair commands, and backup snapshot establishment.
// Restores elsewhere need no archive-owner lock.
func acquireRestoreTargetLock(target string) (func(), error) {
	live, err := restoreTargetsLiveArchive(target)
	if err != nil {
		return nil, err
	}
	if !live {
		return func() {}, nil
	}
	return acquireDirectSQLiteWriteLock(cfg)
}

func restoreTargetsLiveArchive(target string) (bool, error) {
	if cfg == nil || target == "" || cfg.Data.DataDir == "" {
		return false, nil
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false, fmt.Errorf("backup restore: resolving target %q: %w", target, err)
	}
	homeAbs, err := filepath.Abs(cfg.Data.DataDir)
	if err != nil {
		return false, fmt.Errorf("backup restore: resolving data dir %q: %w", cfg.Data.DataDir, err)
	}
	return targetAbs == homeAbs || sameExistingPath(targetAbs, homeAbs), nil
}

// sameExistingPath reports whether a and b name the same existing filesystem
// object even when their spellings differ. filepath.Abs alone compares
// strings, which a case-variant spelling on a case-insensitive filesystem or
// a symlinked path to the archive home would bypass; os.Stat resolves both
// to the object itself. Two paths that do not both exist are not the same
// object — in particular, a live archive home always exists.
func sameExistingPath(a, b string) bool {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}

func runBackupCreate(cmd *cobra.Command, _ []string) error {
	return runBackupCreateLocal(cmd)
}

// backupExtrasSpec builds the msgvault extras selection for the generic
// backup engine: the deletions directory always rides along; config.toml and
// the tokens directory plus client-secret files are opt-in and marked
// sensitive. The flag-named plaintext guard lives here so users see their
// CLI flags in the error; the engine's own sensitive-source guard is the
// backstop.
func backupExtrasSpec() (backup.ExtrasSpec, error) {
	if (backupCreateIncludeConfig || backupCreateIncludeTokens) && !backupCreateAllowPlaintextSecrets {
		var flag string
		switch {
		case backupCreateIncludeConfig && backupCreateIncludeTokens:
			flag = "--include-config/--include-tokens"
		case backupCreateIncludeConfig:
			flag = "--include-config"
		default:
			flag = "--include-tokens"
		}
		return backup.ExtrasSpec{}, fmt.Errorf(
			"%s requires an encrypted repository (use --allow-plaintext-secrets to override)", flag)
	}
	spec := backup.ExtrasSpec{}
	if backupCreateIncludeConfig {
		if cfgPath := cfg.ConfigFilePath(); cfgPath != "" {
			spec.Files = append(spec.Files, backup.ExtrasFileSpec{
				Path: cfgPath, RecordAs: "config.toml", Sensitive: true,
			})
		}
	}
	if backupCreateIncludeTokens {
		spec.Dirs = append(spec.Dirs, backup.ExtrasDirSpec{Name: "tokens", Sensitive: true})
		spec.Globs = append(spec.Globs, backup.ExtrasGlobSpec{Pattern: "client_secret*.json", Sensitive: true})
	}
	return spec, nil
}

func runBackupCreateLocal(cmd *cobra.Command) error {
	repo, err := resolveBackupRepo(backupCreateRepo)
	if err != nil {
		return err
	}
	dbPath := cfg.DatabasePath()
	r, err := backup.Open(repo)
	if err != nil {
		return fmt.Errorf("opening backup repository: %w", err)
	}

	freezer, closeFreezer, err := newBackupFreezer()
	if err != nil {
		return err
	}
	defer closeFreezer()

	// By the time execution reaches here, the client-proxy branch of
	// runBackupCreate has already resolved "auto" to a concrete "bar" or
	// "plain" using its own terminal before forwarding this flag; "auto" only
	// reaches this local-mode fallback in a hypothetical direct (non-proxied)
	// invocation, in which case it resolves from this process's own stdout.
	mode, err := backupProgressModeFromFlag(backupCreateProgress)
	if err != nil {
		return err
	}
	renderer := newBackupProgressRenderer(cmd.OutOrStdout(), mode)
	defer renderer.finish()

	extras, err := backupExtrasSpec()
	if err != nil {
		return err
	}
	m, err := backup.Create(cmd.Context(), r, backupapp.New(Version), backup.CreateOptions{
		DBPath:                dbPath,
		ContentDir:            cfg.AttachmentsDir(),
		DataDir:               cfg.Data.DataDir,
		Extras:                extras,
		IncludeConfig:         backupCreateIncludeConfig,
		IncludeTokens:         backupCreateIncludeTokens,
		AllowPlaintextSecrets: backupCreateAllowPlaintextSecrets,
		Tag:                   backupCreateTag,
		ZstdLevel:             cfg.Backup.ZstdLevel,
		CacheDir:              filepath.Join(cfg.HomeDir, "backup-cache"),
		Freezer:               freezer,
		ForceUnlock:           backupCreateForceUnlock,
		Jobs:                  backupCreateJobs,
		Progress:              renderer.handle,
	})
	if err != nil {
		return fmt.Errorf("creating backup snapshot: %w", err)
	}

	parent := m.ParentID
	if parent == "" {
		parent = "initial"
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created snapshot %s (parent: %s)\n", m.SnapshotID, parent)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Packs added: %d\n", len(m.NewPacks))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Bytes added: %s\n", formatSize(m.BytesAdded))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Duration: %.1fs\n", m.DurationSeconds)
	return nil
}

// newBackupFreezer blocks concurrent direct writers while the backup engine
// takes its SQLite snapshot and captures archive files.
func newBackupFreezer() (backup.FreezeCoordinator, func(), error) {
	return &directBackupFreezer{}, func() {}, nil
}

type directBackupFreezer struct {
	release func()
}

func (f *directBackupFreezer) Begin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.release != nil {
		return errors.New("backup freeze already active")
	}
	release, err := acquireDirectSQLiteWriteLock(cfg)
	if err != nil {
		return err
	}
	f.release = release
	return nil
}

func (f *directBackupFreezer) End(context.Context) error {
	if f.release == nil {
		return errors.New("backup freeze is not active")
	}
	f.release()
	f.release = nil
	return nil
}

func init() {
	backupInitCmd.Flags().StringVar(&backupInitRepo, "repo", "", "Backup repository directory")

	backupCreateCmd.Flags().StringVar(&backupCreateRepo, "repo", "", "Backup repository directory")
	backupCreateCmd.Flags().BoolVar(&backupCreateIncludeConfig, "include-config", false, "Include config.toml verbatim (may contain API keys) in the snapshot")
	backupCreateCmd.Flags().BoolVar(&backupCreateIncludeTokens, "include-tokens", false, "Include the tokens directory in the snapshot")
	backupCreateCmd.Flags().BoolVar(&backupCreateAllowPlaintextSecrets, "allow-plaintext-secrets", false, "Allow capturing secrets in plaintext (required with --include-config/--include-tokens on an unencrypted repository)")
	backupCreateCmd.Flags().StringVar(&backupCreateTag, "tag", "", "Optional label recorded on the snapshot manifest")
	backupCreateCmd.Flags().BoolVar(&backupCreateForceUnlock, "force-unlock", false, "Break a stale exclusive repository lock before creating")
	backupCreateCmd.Flags().IntVar(&backupCreateJobs, "jobs", 0, "Concurrent attachment capture workers (default: one per CPU; use 1 for serial reads on spinning disks or NAS shares)")
	backupCreateCmd.Flags().StringVar(&backupCreateProgress, "progress", "auto", "Progress output mode: auto, bar, or plain")
	_ = backupCreateCmd.Flags().MarkHidden("progress")

	backupListCmd.Flags().StringVar(&backupListRepo, "repo", "", "Backup repository directory")

	backupVerifyCmd.Flags().StringVar(&backupVerifyRepo, "repo", "", "Backup repository directory")
	backupVerifyCmd.Flags().BoolVar(&backupVerifyAll, "all", false, "Verify every snapshot instead of only the latest")
	backupVerifyCmd.Flags().BoolVar(&backupVerifyQuick, "quick", false, "Skip reading and hash-verifying content blobs")
	backupVerifyCmd.Flags().BoolVar(&backupVerifyForceUnlock, "force-unlock", false, "Break a stale exclusive repository lock before verifying")
	backupVerifyCmd.Flags().IntVar(&backupVerifyJobs, "jobs", 0, "Concurrent pack readers for full verify (default: one per CPU; use 1 for serial reads on spinning disks or NAS shares)")

	backupRestoreCmd.Flags().StringVar(&backupRestoreRepo, "repo", "", "Backup repository directory")
	backupRestoreCmd.Flags().StringVar(&backupRestoreTarget, "target", "", "Directory to restore into (required)")
	_ = backupRestoreCmd.MarkFlagRequired("target")
	backupRestoreCmd.Flags().BoolVar(&backupRestoreOverwrite, "overwrite", false, "Allow restoring into a non-empty target directory")
	backupRestoreCmd.Flags().BoolVar(&backupRestoreForceUnlock, "force-unlock", false, "Break a stale exclusive repository lock before restoring")
	backupRestoreCmd.Flags().IntVar(&backupRestoreJobs, "jobs", 0, "Concurrent pack readers (default: one per CPU; use 1 for serial reads on spinning disks or NAS shares)")

	backupCmd.AddCommand(backupInitCmd)
	backupCmd.AddCommand(backupCreateCmd)
	backupCmd.AddCommand(backupListCmd)
	backupCmd.AddCommand(backupVerifyCmd)
	backupCmd.AddCommand(backupRestoreCmd)
	rootCmd.AddCommand(backupCmd)
}
