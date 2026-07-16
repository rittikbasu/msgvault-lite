package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/config"
	"github.com/rittikbasu/msgvault-lite/internal/oauth"
	"github.com/rittikbasu/msgvault-lite/internal/store"
)

func TestAddAccountForceHelpDescribesValidatedReplacement(t *testing.T) {
	cmd := newAddAccountCmd()
	usage := cmd.Flag("force").Usage

	assert.NotContains(t, usage, "Delete existing token")
	assert.Contains(t, usage, "after validation")
}

func TestFindGmailSource(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	tmpDir := t.TempDir()
	s, err := store.Open(tmpDir + "/msgvault.db")
	require.NoError(err, "open store")
	defer func() { _ = s.Close() }()
	require.NoError(s.InitSchema(), "init schema")

	const email = "user@company.com"

	// No sources at all — should suggest add-account.
	src, err := findGmailSource(s, email)
	require.ErrorIs(err, errGmailSourceNotFound, "findGmailSource")
	assert.Nil(src, "expected nil with no sources")

	// Gmail source exists — should suppress the hint.
	_, err = s.GetOrCreateSource("gmail", email)
	require.NoError(err, "create gmail source")
	src, err = findGmailSource(s, email)
	require.NoError(err, "findGmailSource")
	require.NotNil(src, "expected non-nil with gmail source")
	assert.Equal("gmail", src.SourceType, "source type")
}

// TestAddAccount_InheritedBindingValidatesToken verifies that re-running
// add-account without --oauth-app on a named-app account validates the
// token's client_id against the inherited binding.
func TestAddAccount_InheritedBindingValidatesToken(t *testing.T) {
	for _, tc := range []struct {
		name      string
		clientID  string
		wantError bool
	}{
		{"matching token reused", "test.apps.googleusercontent.com", false},
		{"mismatched token rejected", "wrong.apps.googleusercontent.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()
			dbPath := filepath.Join(tmpDir, "msgvault.db")

			s, err := store.Open(dbPath)
			require.NoError(err, "open store")
			require.NoError(s.InitSchema(), "init schema")
			source, err := s.GetOrCreateSource("gmail", "user@acme.com")
			require.NoError(err, "create source")
			require.NoError(s.UpdateSourceOAuthApp(source.ID, sql.NullString{String: "acme", Valid: true}), "set oauth_app")
			_ = s.Close()

			tokensDir := filepath.Join(tmpDir, "tokens")
			require.NoError(os.MkdirAll(tokensDir, 0700), "mkdir")
			tokenData, _ := json.Marshal(map[string]any{
				"access_token":  "fake",
				"refresh_token": "fake",
				"token_type":    "Bearer",
				"client_id":     tc.clientID,
				"scopes":        oauth.Scopes,
			})
			require.NoError(os.WriteFile(filepath.Join(tokensDir, "user@acme.com.json"), tokenData, 0600), "write token")

			secretsPath := filepath.Join(tmpDir, "secret.json")
			require.NoError(os.WriteFile(secretsPath, []byte(fakeClientSecrets), 0600), "write secrets")

			savedCfg, savedLogger, savedOAuthApp := cfg, logger, oauthAppName
			defer func() { cfg, logger, oauthAppName = savedCfg, savedLogger, savedOAuthApp }()

			cfg = &config.Config{
				HomeDir: tmpDir,
				Data:    config.DataConfig{DataDir: tmpDir},
				OAuth: config.OAuthConfig{
					Apps: map[string]config.OAuthApp{
						"acme": {ClientSecrets: secretsPath},
					},
				},
			}
			logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			testCmd := &cobra.Command{
				Use: "add-account <email>", Args: cobra.ExactArgs(1),
				RunE: addAccountCmd.RunE,
			}
			testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
			testCmd.Flags().BoolVar(&headless, "headless", false, "")
			testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
			testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

			root := newTestRootCmd()
			root.AddCommand(testCmd)
			// No --oauth-app flag: binding inherited from DB
			root.SetArgs([]string{"add-account", "user@acme.com"})

			err = root.ExecuteContext(ctx)
			if tc.wantError {
				require.Error(err, "expected error for mismatched token")
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestAddAccount_CalendarOnlyTokenRequiresGmailReauth(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	tokensDir := filepath.Join(tmpDir, "tokens")
	require.NoError(os.MkdirAll(tokensDir, 0700), "mkdir tokens")
	tokenData, err := json.Marshal(map[string]any{
		"access_token":  "fake-access",
		"refresh_token": "fake-refresh",
		"token_type":    "Bearer",
		"client_id":     "test.apps.googleusercontent.com",
		"scopes":        []string{"https://www.googleapis.com/auth/calendar.readonly"},
	})
	require.NoError(err, "marshal token")
	require.NoError(os.WriteFile(filepath.Join(tokensDir, "user@example.com.json"), tokenData, 0600), "write token")

	secretsPath := filepath.Join(tmpDir, "secret.json")
	require.NoError(os.WriteFile(secretsPath, []byte(fakeClientSecrets), 0600), "write secrets")

	savedCfg, savedLogger, savedOAuthApp := cfg, logger, oauthAppName
	defer func() { cfg, logger, oauthAppName = savedCfg, savedLogger, savedOAuthApp }()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth:   config.OAuthConfig{ClientSecrets: secretsPath},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	testCmd := &cobra.Command{
		Use: "add-account <email>", Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{"add-account", "user@example.com"})

	err = root.ExecuteContext(ctx)
	require.Error(err, "calendar-only token must trigger Gmail reauthorization")

	s, err := store.Open(filepath.Join(tmpDir, "msgvault.db"))
	require.NoError(err, "open store")
	defer func() { _ = s.Close() }()
	require.NoError(s.InitSchema(), "init schema")
	src, err := findGmailSource(s, "user@example.com")
	require.ErrorIs(err, errGmailSourceNotFound)
	require.Nil(src)
}

func TestAddAccount_OverprivilegedGmailTokenRequiresReauth(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "msgvault.db")

	tokensDir := filepath.Join(tmpDir, "tokens")
	require.NoError(os.MkdirAll(tokensDir, 0700), "mkdir tokens")
	tokenData, err := json.Marshal(map[string]any{
		"access_token":  "fake-access",
		"refresh_token": "fake-refresh",
		"token_type":    "Bearer",
		"client_id":     "test.apps.googleusercontent.com",
		"scopes": []string{
			oauth.ScopeGmailReadonly,
			"https://www.googleapis.com/auth/gmail.modify",
		},
	})
	require.NoError(err, "marshal token")
	require.NoError(os.WriteFile(filepath.Join(tokensDir, "user@example.com.json"), tokenData, 0600), "write token")

	secretsPath := filepath.Join(tmpDir, "secret.json")
	require.NoError(os.WriteFile(secretsPath, []byte(fakeClientSecrets), 0600), "write secrets")

	savedCfg, savedLogger, savedOAuthApp := cfg, logger, oauthAppName
	savedHeadless, savedForceReauth := headless, forceReauth
	savedDisplayName := accountDisplayName
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		oauthAppName = savedOAuthApp
		headless = savedHeadless
		forceReauth = savedForceReauth
		accountDisplayName = savedDisplayName
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth:   config.OAuthConfig{ClientSecrets: secretsPath},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	testCmd := &cobra.Command{
		Use: "add-account <email>", Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{"add-account", "user@example.com"})

	require.Error(root.ExecuteContext(ctx), "overprivileged token must trigger readonly reauthorization")

	s, err := store.Open(dbPath)
	require.NoError(err, "open store")
	defer func() { _ = s.Close() }()
	require.NoError(s.InitSchema(), "init schema")
	src, err := findGmailSource(s, "user@example.com")
	require.ErrorIs(err, errGmailSourceNotFound)
	require.Nil(src)
}

// TestAddAccount_RebindWithExistingToken verifies that switching
// OAuth app binding with an existing token updates the binding
// without re-authorizing (headless rebind scenario).
func TestAddAccount_RebindWithExistingToken(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "msgvault.db")

	// Set up DB with a source bound to "old-app"
	s, err := store.Open(dbPath)
	require.NoError(err, "open store")
	require.NoError(s.InitSchema(), "init schema")
	source, err := s.GetOrCreateSource("gmail", "user@acme.com")
	require.NoError(err, "create source")
	require.NoError(s.UpdateSourceOAuthApp(source.ID, sql.NullString{
		String: "old-app", Valid: true,
	}), "set oauth_app")
	_ = s.Close()

	// Write a fake token file
	tokensDir := filepath.Join(tmpDir, "tokens")
	require.NoError(os.MkdirAll(tokensDir, 0700), "mkdir tokens")
	// client_id must match the fake client secrets so
	// TokenMatchesClient returns true (headless rebind scenario).
	tokenData, err := json.Marshal(map[string]any{
		"access_token":  "fake-access",
		"refresh_token": "fake-refresh",
		"token_type":    "Bearer",
		"client_id":     "test.apps.googleusercontent.com",
		"scopes":        oauth.Scopes,
	})
	require.NoError(err, "marshal token")
	tokenPath := filepath.Join(tokensDir, "user@acme.com.json")
	require.NoError(os.WriteFile(tokenPath, tokenData, 0600), "write token")

	// Write fake client secrets
	secretsPath := filepath.Join(tmpDir, "secret.json")
	require.NoError(os.WriteFile(
		secretsPath, []byte(fakeClientSecrets), 0600,
	), "write secrets")

	savedCfg := cfg
	savedLogger := logger
	savedOAuthApp := oauthAppName
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		oauthAppName = savedOAuthApp
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth: config.OAuthConfig{
			Apps: map[string]config.OAuthApp{
				"old-app": {ClientSecrets: secretsPath},
				"new-app": {ClientSecrets: secretsPath},
			},
		},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	testCmd := &cobra.Command{
		Use:  "add-account <email>",
		Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{
		"add-account", "user@acme.com", "--oauth-app", "new-app",
	})

	// Should succeed without opening a browser — token exists
	err = root.Execute()
	require.NoError(err)

	// Token file should still exist
	_, statErr := os.Stat(tokenPath)
	assert.False(os.IsNotExist(statErr), "token file was deleted during rebind")

	// Binding should be updated to "new-app"
	s2, err := store.Open(dbPath)
	require.NoError(err, "reopen store")
	defer func() { _ = s2.Close() }()

	src, err := findGmailSource(s2, "user@acme.com")
	require.NoError(err, "find source")
	require.NotNil(src, "source not found after rebind")
	assert.True(src.OAuthApp.Valid && src.OAuthApp.String == "new-app",
		"oauth_app = %v, want new-app", src.OAuthApp)
}

// TestAddAccount_ForceRebindPreservesBindingOnFailure verifies that
// --force --oauth-app with a cancelled auth does not update the binding.
// TestAddAccount_NewRegistrationRejectsMismatchedToken verifies that
// add-account --oauth-app with no existing source row rejects a token
// minted by a different OAuth client (forces re-auth, not silent accept).
func TestAddAccount_NewRegistrationRejectsMismatchedToken(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	// Write a token with a DIFFERENT client_id than the fake secrets
	tokensDir := filepath.Join(tmpDir, "tokens")
	require.NoError(os.MkdirAll(tokensDir, 0700), "mkdir tokens")
	tokenData, err := json.Marshal(map[string]string{
		"access_token":  "fake-access",
		"refresh_token": "fake-refresh",
		"token_type":    "Bearer",
		"client_id":     "wrong-client.apps.googleusercontent.com",
	})
	require.NoError(err, "marshal token")
	require.NoError(os.WriteFile(
		filepath.Join(tokensDir, "new@acme.com.json"),
		tokenData, 0600,
	), "write token")

	secretsPath := filepath.Join(tmpDir, "secret.json")
	require.NoError(os.WriteFile(
		secretsPath, []byte(fakeClientSecrets), 0600,
	), "write secrets")

	savedCfg := cfg
	savedLogger := logger
	savedOAuthApp := oauthAppName
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		oauthAppName = savedOAuthApp
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth: config.OAuthConfig{
			Apps: map[string]config.OAuthApp{
				"acme": {ClientSecrets: secretsPath},
			},
		},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Pre-cancel so if it falls through to Authorize, it fails fast
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	testCmd := &cobra.Command{
		Use:  "add-account <email>",
		Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{
		"add-account", "new@acme.com", "--oauth-app", "acme",
	})

	// Should fail: token exists but from wrong client, auth cancelled
	err = root.ExecuteContext(ctx)
	require.Error(err, "mismatched token should not be silently accepted")
}

// TestAddAccount_ExplicitDefaultRejectsMismatchedToken verifies that
// --oauth-app "" rejects a token minted by a different client.
func TestAddAccount_ExplicitDefaultRejectsMismatchedToken(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	tokensDir := filepath.Join(tmpDir, "tokens")
	require.NoError(os.MkdirAll(tokensDir, 0700), "mkdir tokens")
	// Token with a client_id that does NOT match the default secrets
	tokenData, err := json.Marshal(map[string]string{
		"access_token":  "fake-access",
		"refresh_token": "fake-refresh",
		"token_type":    "Bearer",
		"client_id":     "wrong-client.apps.googleusercontent.com",
	})
	require.NoError(err, "marshal token")
	require.NoError(os.WriteFile(
		filepath.Join(tokensDir, "user@example.com.json"),
		tokenData, 0600,
	), "write token")

	secretsPath := filepath.Join(tmpDir, "secret.json")
	require.NoError(os.WriteFile(
		secretsPath, []byte(fakeClientSecrets), 0600,
	), "write secrets")

	savedCfg := cfg
	savedLogger := logger
	savedOAuthApp := oauthAppName
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		oauthAppName = savedOAuthApp
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth:   config.OAuthConfig{ClientSecrets: secretsPath},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	testCmd := &cobra.Command{
		Use:  "add-account <email>",
		Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{
		"add-account", "user@example.com", "--oauth-app", "",
	})

	err = root.ExecuteContext(ctx)
	require.Error(err, "mismatched token should be rejected with explicit --oauth-app \"\"")
}

// TestAddAccount_ExplicitDefaultAcceptsMatchingToken verifies that
// --oauth-app "" accepts a token minted by the default client.
func TestAddAccount_ExplicitDefaultAcceptsMatchingToken(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	tokensDir := filepath.Join(tmpDir, "tokens")
	require.NoError(os.MkdirAll(tokensDir, 0700), "mkdir tokens")
	// Token with client_id matching the fake secrets
	tokenData, err := json.Marshal(map[string]any{
		"access_token":  "fake-access",
		"refresh_token": "fake-refresh",
		"token_type":    "Bearer",
		"client_id":     "test.apps.googleusercontent.com",
		"scopes":        oauth.Scopes,
	})
	require.NoError(err, "marshal token")
	require.NoError(os.WriteFile(
		filepath.Join(tokensDir, "user@example.com.json"),
		tokenData, 0600,
	), "write token")

	secretsPath := filepath.Join(tmpDir, "secret.json")
	require.NoError(os.WriteFile(
		secretsPath, []byte(fakeClientSecrets), 0600,
	), "write secrets")

	savedCfg := cfg
	savedLogger := logger
	savedOAuthApp := oauthAppName
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		oauthAppName = savedOAuthApp
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth:   config.OAuthConfig{ClientSecrets: secretsPath},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	testCmd := &cobra.Command{
		Use:  "add-account <email>",
		Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	// Pre-cancel so if regression causes auth attempt, it fails fast
	// instead of opening a browser.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{
		"add-account", "user@example.com", "--oauth-app", "",
	})

	// Should succeed: token's client_id matches, no auth needed
	err = root.ExecuteContext(ctx)
	require.NoError(err)
}

func TestAddAccount_ForceRebindPreservesBindingOnFailure(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "msgvault.db")

	s, err := store.Open(dbPath)
	require.NoError(err, "open store")
	require.NoError(s.InitSchema(), "init schema")
	source, err := s.GetOrCreateSource("gmail", "user@acme.com")
	require.NoError(err, "create source")
	require.NoError(s.UpdateSourceOAuthApp(source.ID, sql.NullString{
		String: "old-app", Valid: true,
	}), "set oauth_app")
	_ = s.Close()

	secretsPath := filepath.Join(tmpDir, "secret.json")
	require.NoError(os.WriteFile(
		secretsPath, []byte(fakeClientSecrets), 0600,
	), "write secrets")

	savedCfg := cfg
	savedLogger := logger
	savedOAuthApp := oauthAppName
	savedForce := forceReauth
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		oauthAppName = savedOAuthApp
		forceReauth = savedForce
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth: config.OAuthConfig{
			Apps: map[string]config.OAuthApp{
				"old-app": {ClientSecrets: secretsPath},
				"new-app": {ClientSecrets: secretsPath},
			},
		},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Pre-cancel context so Authorize fails immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	testCmd := &cobra.Command{
		Use:  "add-account <email>",
		Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{
		"add-account", "user@acme.com",
		"--force", "--oauth-app", "new-app",
	})

	err = root.ExecuteContext(ctx)
	require.Error(err, "expected error from cancelled auth")

	// Binding should still be "old-app"
	s2, err := store.Open(dbPath)
	require.NoError(err, "reopen store")
	defer func() { _ = s2.Close() }()

	src, err := findGmailSource(s2, "user@acme.com")
	require.NoError(err, "find source")
	require.NotNil(src, "source not found")
	assert.True(t, src.OAuthApp.Valid && src.OAuthApp.String == "old-app",
		"oauth_app = %v, want old-app (binding should not change on auth failure)", src.OAuthApp)
}

// TestAddAccount_HeadlessExplicitEmptyOAuthApp verifies that
// --headless --oauth-app "" does not re-inherit the stored binding.
func TestAddAccount_HeadlessExplicitEmptyOAuthApp(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "msgvault.db")

	// Set up DB with a source bound to "acme"
	s, err := store.Open(dbPath)
	require.NoError(err, "open store")
	require.NoError(s.InitSchema(), "init schema")
	source, err := s.GetOrCreateSource("gmail", "user@acme.com")
	require.NoError(err, "create source")
	require.NoError(s.UpdateSourceOAuthApp(source.ID, sql.NullString{
		String: "acme", Valid: true,
	}), "set oauth_app")
	_ = s.Close()

	// Save/restore globals
	savedCfg := cfg
	savedLogger := logger
	savedHeadless := headless
	savedOAuthApp := oauthAppName
	savedForce := forceReauth
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		headless = savedHeadless
		oauthAppName = savedOAuthApp
		forceReauth = savedForce
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	// The RunE reads package-level flag vars, but uses
	// cmd.Flags().Changed() to detect explicit --oauth-app.
	// We need to register the flags on the test command so
	// Changed() works, and bind them to the package-level vars.
	testCmd := &cobra.Command{
		Use:  "add-account <email>",
		Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{
		"add-account", "user@acme.com",
		"--headless", "--oauth-app", "",
	})

	getOutput := captureStdout(t)
	err = root.Execute()
	output := getOutput()

	require.NoError(err)

	// The output should NOT contain --oauth-app acme since we
	// explicitly passed an empty --oauth-app to clear to default.
	assert.NotContains(t, output, "--oauth-app",
		"explicit empty --oauth-app should not inherit stored binding; output:\n%s", output)
}

// TestAddAccount_AutoDefaultIdentityFires verifies that running add-account
// with a reusable token writes an account-identifier identity row.
func TestAddAccount_HeadlessServiceAccountReturnsActionableError(t *testing.T) {
	tmpDir := t.TempDir()

	savedCfg := cfg
	savedLogger := logger
	savedHeadless := headless
	savedOAuthApp := oauthAppName
	savedForce := forceReauth
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		headless = savedHeadless
		oauthAppName = savedOAuthApp
		forceReauth = savedForce
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth: config.OAuthConfig{
			Apps: map[string]config.OAuthApp{
				"workspace": {ServiceAccountKey: filepath.Join(tmpDir, "service-account.json")},
			},
		},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	testCmd := &cobra.Command{
		Use:  "add-account <email>",
		Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{
		"add-account", "user@company.com",
		"--headless", "--oauth-app", "workspace",
	})

	getOutput := captureStdout(t)
	err := root.Execute()
	output := getOutput()

	require.Error(t, err, "expected --headless service account error")
	require.ErrorContains(t, err, "service accounts do not use --headless")
	assert.NotContains(t, output, "Headless Server Setup",
		"service account path should not print browser OAuth headless instructions:\n%s", output)
}

func TestAddAccount_ForceServiceAccountReturnsActionableError(t *testing.T) {
	tmpDir := t.TempDir()

	savedCfg := cfg
	savedLogger := logger
	savedHeadless := headless
	savedOAuthApp := oauthAppName
	savedForce := forceReauth
	defer func() {
		cfg = savedCfg
		logger = savedLogger
		headless = savedHeadless
		oauthAppName = savedOAuthApp
		forceReauth = savedForce
	}()

	cfg = &config.Config{
		HomeDir: tmpDir,
		Data:    config.DataConfig{DataDir: tmpDir},
		OAuth: config.OAuthConfig{
			Apps: map[string]config.OAuthApp{
				"workspace": {ServiceAccountKey: filepath.Join(tmpDir, "service-account.json")},
			},
		},
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	testCmd := &cobra.Command{
		Use:  "add-account <email>",
		Args: cobra.ExactArgs(1),
		RunE: addAccountCmd.RunE,
	}
	testCmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "")
	testCmd.Flags().BoolVar(&headless, "headless", false, "")
	testCmd.Flags().BoolVar(&forceReauth, "force", false, "")
	testCmd.Flags().StringVar(&accountDisplayName, "display-name", "", "")

	root := newTestRootCmd()
	root.AddCommand(testCmd)
	root.SetArgs([]string{
		"add-account", "user@company.com",
		"--force", "--oauth-app", "workspace",
	})

	err := root.Execute()
	require.Error(t, err, "expected --force service account error")
	require.ErrorContains(t, err, "service accounts do not use --force")
}

func TestResolveAddAccountBinding(t *testing.T) {
	app := func(name string) sql.NullString {
		return sql.NullString{String: name, Valid: true}
	}
	cases := []struct {
		name         string
		flagApp      string
		flagExplicit bool
		storedApp    sql.NullString
		sourceExists bool
		want         addAccountBinding
	}{
		{
			name: "new account with defaults",
			want: addAccountBinding{},
		},
		{
			name:         "inherits stored binding without flag",
			storedApp:    app("acme"),
			sourceExists: true,
			want:         addAccountBinding{resolvedApp: "acme"},
		},
		{
			name:         "explicit flag overrides stored binding",
			flagApp:      "other",
			flagExplicit: true,
			storedApp:    app("acme"),
			sourceExists: true,
			want:         addAccountBinding{resolvedApp: "other", explicit: true, bindingChanged: true},
		},
		{
			name:         "explicit clear of stored binding",
			flagApp:      "",
			flagExplicit: true,
			storedApp:    app("acme"),
			sourceExists: true,
			want:         addAccountBinding{explicit: true, bindingChanged: true},
		},
		{
			name:         "explicit flag matching stored binding",
			flagApp:      "acme",
			flagExplicit: true,
			storedApp:    app("acme"),
			sourceExists: true,
			want:         addAccountBinding{resolvedApp: "acme", explicit: true},
		},
		{
			name:         "explicit flag on new account",
			flagApp:      "acme",
			flagExplicit: true,
			want:         addAccountBinding{resolvedApp: "acme", explicit: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAddAccountBinding(tc.flagApp, tc.flagExplicit, tc.storedApp, tc.sourceExists)
			assert.Equal(t, tc.want, got)
		})
	}
}
