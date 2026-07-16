package cmd

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/rittikbasu/msgvault-lite/internal/config"
	"github.com/rittikbasu/msgvault-lite/internal/fileutil"
	"github.com/rittikbasu/msgvault-lite/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClientSecrets is a minimal Google OAuth client_secret.json that
// oauth.NewManager can parse. No real credentials are exposed.
const fakeClientSecrets = `{
  "installed": {
    "client_id": "test.apps.googleusercontent.com",
    "client_secret": "test-secret",
    "auth_uri": "https://accounts.google.com/o/oauth2/auth",
    "token_uri": "https://oauth2.googleapis.com/token",
    "redirect_uris": ["http://localhost"]
  }
}`

func runSyncFullLocalForTest(cmd *cobra.Command, args []string) error {
	if err := validateSyncFullFlags(cmd); err != nil {
		return err
	}
	return runSyncFullLocal(cmd, args)
}

// malformed --after flag is rejected before any source is synced.
func TestSyncFullCmd_MalformedDateRejectsBeforeSync(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/msgvault.db"

	s, err := store.Open(dbPath)
	require.NoError(err, "open store")
	require.NoError(s.InitSchema(), "init schema")

	_, err = s.GetOrCreateSource("gmail", "g@example.com")
	require.NoError(err, "create gmail source")
	_ = s.Close()

	// Write OAuth client secrets and a fake token so the Gmail
	// source passes discovery checks (HasAnyConfig + HasToken).
	secretsPath := filepath.Join(tmpDir, "client_secret.json")
	require.NoError(fileutil.SecureWriteFile(secretsPath, []byte(fakeClientSecrets), 0600), "write client secrets")
	tokensDir := filepath.Join(tmpDir, "tokens")
	require.NoError(os.MkdirAll(tokensDir, 0700), "create tokens dir")
	fakeToken := `{"access_token":"fake","token_type":"Bearer"}`
	require.NoError(fileutil.SecureWriteFile(filepath.Join(tokensDir, "g@example.com.json"), []byte(fakeToken), 0600), "write fake token")

	savedCfg := cfg
	savedLogger := logger
	savedAfter := syncAfter
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		syncAfter = savedAfter
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth:   config.OAuthConfig{ClientSecrets: secretsPath},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	syncAfter = "not-a-date"

	testCmd := &cobra.Command{
		Use:  "sync-full [email]",
		Args: cobra.MaximumNArgs(1),
		RunE: runSyncFullLocalForTest,
	}

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{"sync-full"})

	getOutput := captureStdout(t)
	err = root.Execute()
	output := getOutput()

	require.Error(err, "expected error for malformed date")
	require.ErrorContains(err, "--after")
	// No source should have been attempted — the date error
	// must fire before source discovery, not after Gmail syncs.
	assert.NotContains(output, "Starting full sync", "no sync should start when date flag is invalid")
}

// TestSyncCmd_GmailOnlyBrokenOAuthSurfacesError verifies that when
// only Gmail sources exist and OAuth is broken, the actual error is
// returned, not "no accounts are ready to sync".
func TestSyncCmd_GmailOnlyBrokenOAuthSurfacesError(t *testing.T) {
	for _, tc := range []struct {
		name string
		runE func(*cobra.Command, []string) error
	}{
		{"sync", runSyncIncrementalLocal},
		{"sync-full", runSyncFullLocalForTest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()
			dbPath := tmpDir + "/msgvault.db"

			s, err := store.Open(dbPath)
			require.NoError(err, "open store")
			require.NoError(s.InitSchema(), "init schema")

			gmailSrc, err := s.GetOrCreateSource(
				"gmail", "g@example.com",
			)
			require.NoError(err, "create source")
			require.NoError(s.UpdateSourceSyncCursor(gmailSrc.ID, "1"), "set cursor")
			_ = s.Close()

			secretsPath := filepath.Join(
				tmpDir, "client_secret.json",
			)
			require.NoError(fileutil.SecureWriteFile(
				secretsPath, []byte("not json"), 0600,
			), "write secrets")

			savedCfg := cfg
			savedLogger := logger
			defer func() {
				cfg = savedCfg
				logger = savedLogger
			}()

			cfg = &config.Config{
				HomeDir: tmpDir,
				Data:    config.DataConfig{DataDir: tmpDir},
				OAuth: config.OAuthConfig{
					ClientSecrets: secretsPath,
				},
			}
			logger = slog.New(
				slog.NewTextHandler(os.Stderr, nil),
			)

			testCmd := &cobra.Command{
				Use:  tc.name + " [email]",
				Args: cobra.MaximumNArgs(1),
				RunE: tc.runE,
			}

			root := newTestRootCmd()
			root.AddCommand(testCmd)
			root.SetArgs([]string{tc.name})

			err = root.Execute()
			require.Error(err, "expected error")

			errMsg := err.Error()

			// Should surface the real OAuth parse error.
			assert.Contains(t, errMsg, "client secrets", "expected OAuth parse error")
		})
	}
}
