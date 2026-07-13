package cmd

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/oauth"
	"go.kenn.io/msgvault/internal/store"
)

var (
	headless                    bool
	accountDisplayName          string
	forceReauth                 bool
	oauthAppName                string
	noDefaultIdentityAddAccount bool
)

// addAccountUse is the usage string for the add-account command.
const addAccountUse = "add-account <email>"

// errGmailSourceNotFound is returned by findGmailSource when no Gmail
// source is registered for the given identifier. Wrapped via fmt.Errorf
// so callers can use errors.Is to tell "no such account" apart from real
// lookup errors.
var errGmailSourceNotFound = errors.New("gmail source not found")

var addAccountCmd = &cobra.Command{
	Use:   addAccountUse,
	Short: "Add a Gmail account via OAuth",
	Long: `Add a Gmail account by completing the OAuth2 authorization flow.

By default, opens a browser for authorization. Use --headless to see instructions
for authorizing on headless servers (Google does not support Gmail in device flow).

If a token already exists, the command skips authorization. Use --force to obtain
and validate a replacement before atomically replacing the existing token.

For Google Workspace orgs that require their own OAuth app, use --oauth-app to
specify a named app from config.toml.

Examples:
  msgvault add-account you@gmail.com
  msgvault add-account you@gmail.com --headless
 msgvault add-account you@gmail.com --force
  msgvault add-account you@acme.com --oauth-app acme
  msgvault add-account you@gmail.com --display-name "Work Account"`,
	Args: cobra.ExactArgs(1),
	RunE: runAddAccountLocal,
}

func newAddAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   addAccountUse,
		Short: addAccountCmd.Short,
		Long:  addAccountCmd.Long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if headless && forceReauth {
				return usageErr(cmd, errors.New("--headless and --force cannot be used together: --force requires browser-based OAuth which is not available in headless mode"))
			}
			return runAddAccountLocal(cmd, args)
		},
	}
	registerAddAccountFlags(cmd)
	return cmd
}

// addAccountBinding is the resolved OAuth app for an add-account run.
type addAccountBinding struct {
	resolvedApp    string
	explicit       bool
	bindingChanged bool
}

// resolveAddAccountBinding inherits the stored oauth_app binding when the
// flag is absent (so re-adding a named-app account after token loss uses
// the correct credentials) and detects explicit binding changes, including
// clearing back to the default app.
func resolveAddAccountBinding(flagApp string, flagExplicit bool, storedApp sql.NullString, sourceExists bool) addAccountBinding {
	binding := addAccountBinding{resolvedApp: flagApp, explicit: flagExplicit}
	if !flagExplicit && sourceExists && storedApp.Valid {
		binding.resolvedApp = storedApp.String
	}
	if flagExplicit && sourceExists {
		currentApp := ""
		if storedApp.Valid {
			currentApp = storedApp.String
		}
		if currentApp != flagApp {
			binding.bindingChanged = true
		}
	}
	return binding
}

// newAddAccountOAuthManager builds a Gmail manager that requests only the
// exact read-only grant, replacing any legacy broader token on reauthorization.
func newAddAccountOAuthManager(clientSecretsPath, _ string) (*oauth.Manager, error) {
	mgr, err := oauth.NewManager(clientSecretsPath, cfg.TokensDir(), logger)
	if err != nil {
		return nil, wrapOAuthError(fmt.Errorf("create oauth manager: %w", err))
	}
	return mgr, nil
}

// addAccountTokenReusable reports whether the stored token can be reused
// without a fresh authorization. The token's client identity is validated
// whenever any named app is involved — from an explicit flag, a binding
// change, or a stored binding — because a mismatched token would fail on
// its next refresh.
func addAccountTokenReusable(mgr *oauth.Manager, email string, binding addAccountBinding) bool {
	_ = binding
	return mgr.HasToken(email) &&
		mgr.TokenMatchesClient(email) &&
		addAccountTokenHasGmailScopes(mgr, email)
}

// addAccountAuthorizeError decorates an authorization failure with the
// re-add hint when the consent screen authenticated a different address
// than the one being added.
func addAccountAuthorizeError(err error, sourceExists bool) error {
	var mismatch *oauth.TokenMismatchError
	if errors.As(err, &mismatch) && !sourceExists {
		return fmt.Errorf(
			"%w\nIf %s is the primary address, re-add with:\n"+
				"  msgvault add-account %s",
			err, mismatch.Actual, mismatch.Actual,
		)
	}
	return fmt.Errorf("authorization failed: %w", err)
}

func validateSingleGmailArchive(s *store.Store, email string) error {
	sources, err := s.ListSources("")
	if err != nil {
		return fmt.Errorf("list configured sources: %w", err)
	}
	for _, source := range sources {
		if source.SourceType != sourceTypeGmail {
			return fmt.Errorf("archive contains unsupported source type %q; msgvault-lite supports exactly one Gmail source", source.SourceType)
		}
		if source.Identifier != email {
			return fmt.Errorf("Gmail account %s is already configured; msgvault-lite supports exactly one account", source.Identifier)
		}
	}
	return nil
}

func runAddAccountLocal(cmd *cobra.Command, args []string) error {
	email := args[0]

	if headless && forceReauth {
		return usageErr(cmd, errors.New("--headless and --force cannot be used together: --force requires browser-based OAuth which is not available in headless mode"))
	}

	oauthAppExplicit := cmd.Flags().Changed("oauth-app")
	var clientSecretsPath string

	// Initialize database (in case it's new)
	s, cleanup, err := openWritableStoreAndInitForIngest()
	if err != nil {
		return err
	}
	defer cleanup()

	if err := validateSingleGmailArchive(s, email); err != nil {
		return err
	}

	// Look up existing source to detect binding changes
	existingSource, err := findGmailSource(s, email)
	if err != nil && !errors.Is(err, errGmailSourceNotFound) {
		return fmt.Errorf("look up existing source: %w", err)
	}

	storedApp := sql.NullString{}
	if existingSource != nil {
		storedApp = existingSource.OAuthApp
	}
	binding := resolveAddAccountBinding(oauthAppName, oauthAppExplicit, storedApp, existingSource != nil)
	resolvedApp := binding.resolvedApp
	bindingChanged := binding.bindingChanged

	saKeyPath := cfg.OAuth.ServiceAccountKeyFor(resolvedApp)
	if headless {
		if saKeyPath != "" {
			return usageErr(cmd, errors.New("service accounts do not use --headless; run add-account without --headless"))
		}
		oauth.PrintHeadlessInstructions(email, cfg.TokensDir(), resolvedApp)
		return nil
	}

	// Check for service account configuration first
	if saKeyPath != "" {
		if forceReauth {
			return usageErr(cmd, errors.New("service accounts do not use --force; tokens are minted on demand from the configured service account key"))
		}
		saMgr, saErr := oauth.NewServiceAccountManager(saKeyPath)
		if saErr != nil {
			return fmt.Errorf("service account: %w", saErr)
		}

		// Validate access by calling Gmail profile API
		ts, saErr := saMgr.TokenSource(cmd.Context(), email)
		if saErr != nil {
			return fmt.Errorf("service account token for %s: %w", email, saErr)
		}
		if saErr := oauth.ValidateTokenEmail(cmd.Context(), ts, email); saErr != nil {
			var mismatch *oauth.TokenMismatchError
			if errors.As(saErr, &mismatch) {
				existing, lookupErr := findGmailSource(s, email)
				if lookupErr != nil && !errors.Is(lookupErr, errGmailSourceNotFound) {
					return fmt.Errorf("service account validation failed: %w (also: %w)", saErr, lookupErr)
				}
				if existing == nil {
					return fmt.Errorf(
						"%w\nIf %s is the primary address, re-add with:\n"+
							"  msgvault add-account %s",
						saErr, mismatch.Actual, mismatch.Actual,
					)
				}
			}
			return fmt.Errorf("service account validation for %s: %w", email, saErr)
		}

		// Register source
		source, saErr := s.GetOrCreateSource(sourceTypeGmail, email)
		if saErr != nil {
			return fmt.Errorf("create source: %w", saErr)
		}
		// Persist the oauth_app binding (set or clear). Mirror the
		// standard OAuth branch: when --oauth-app was explicitly
		// changed and resolves to "", clear the stored binding so
		// later syncs don't keep resolving credentials through the
		// stale named-app pointer.
		if resolvedApp != "" {
			newApp := sql.NullString{String: resolvedApp, Valid: true}
			if saErr := s.UpdateSourceOAuthApp(source.ID, newApp); saErr != nil {
				return fmt.Errorf("update oauth app binding: %w", saErr)
			}
		} else if bindingChanged {
			if saErr := s.UpdateSourceOAuthApp(source.ID, sql.NullString{}); saErr != nil {
				return fmt.Errorf("clear oauth app binding: %w", saErr)
			}
		}
		if accountDisplayName != "" {
			if saErr := s.UpdateSourceDisplayName(source.ID, accountDisplayName); saErr != nil {
				return fmt.Errorf("set display name: %w", saErr)
			}
		}

		fmt.Printf("Account %s authorized via service account.\n", email)
		fmt.Println("Next step: msgvault sync-full", email)
		return nil
	}

	// Resolve client secrets path (standard OAuth flow)
	clientSecretsPath, err = cfg.OAuth.ClientSecretsFor(resolvedApp)
	if err != nil {
		if !cfg.OAuth.HasAnyConfig() {
			return errOAuthNotConfigured()
		}
		return err
	}

	// Create OAuth manager. If a scoped token already exists, preserve those
	// grants when reauthorizing for Gmail; Google replacement consent would
	// otherwise drop Calendar/Drive scopes from the shared token file.
	oauthMgr, err := newAddAccountOAuthManager(clientSecretsPath, email)
	if err != nil {
		return err
	}

	tokenReusable := !forceReauth && addAccountTokenReusable(oauthMgr, email, binding)
	if tokenReusable {
		source, err := s.GetOrCreateSource(sourceTypeGmail, email)
		if err != nil {
			return fmt.Errorf("create source: %w", err)
		}
		// Update oauth_app binding if it changed or was newly specified
		if bindingChanged || (resolvedApp != "" && !source.OAuthApp.Valid) {
			newApp := sql.NullString{String: resolvedApp, Valid: resolvedApp != ""}
			if err := s.UpdateSourceOAuthApp(source.ID, newApp); err != nil {
				return fmt.Errorf("update oauth app binding: %w", err)
			}
		}
		if accountDisplayName != "" {
			if err := s.UpdateSourceDisplayName(source.ID, accountDisplayName); err != nil {
				return fmt.Errorf("set display name: %w", err)
			}
		}
		// Auto-default-identity must run BEFORE the legacy migration
		// retry (runPostSourceCreateMigrations). The migration's
		// set-semantics merge handles the case where the legacy
		// [identity] block contains the same address. Reverse order
		// would leave the source without its own account identifier
		// because confirmDefaultIdentity skips on any existing rows.
		if !noDefaultIdentityAddAccount {
			confirmDefaultIdentity(cmd.OutOrStdout(), s, source.ID, email, email, "account-identifier")
		}
		if err := runPostSourceCreateMigrations(s); err != nil {
			return fmt.Errorf("post-source-create migrations: %w", err)
		}
		if bindingChanged {
			fmt.Printf("Account %s: OAuth app binding updated to %q.\n", email, resolvedApp)
		} else {
			fmt.Printf("Account %s is already authorized.\n", email)
		}
		fmt.Println("Next step: msgvault sync-full", email)
		return nil
	}

	// Perform authorization. Local frontends preflight the browser flow
	// before proxying, so a subprocess normally finds a fresh token above;
	// this path still runs for remote daemons, where the browser opens on
	// the daemon's host exactly as it did before daemon routing.
	if bindingChanged {
		fmt.Printf("Switching OAuth app for %s to %q. Authorizing...\n", email, oauthAppName)
	} else {
		fmt.Println("Starting browser authorization...")
	}

	if err := oauthMgr.Authorize(cmd.Context(), email); err != nil {
		return addAccountAuthorizeError(err, existingSource != nil)
	}

	// Authorization succeeded — now persist the binding and source.
	source, err := s.GetOrCreateSource(sourceTypeGmail, email)
	if err != nil {
		return fmt.Errorf("create source: %w", err)
	}

	// Update oauth_app binding (set or clear)
	if resolvedApp != "" {
		newApp := sql.NullString{String: resolvedApp, Valid: true}
		if err := s.UpdateSourceOAuthApp(source.ID, newApp); err != nil {
			return fmt.Errorf("update oauth app binding: %w", err)
		}
	} else if bindingChanged {
		// Clearing the binding (switching back to default)
		if err := s.UpdateSourceOAuthApp(source.ID, sql.NullString{}); err != nil {
			return fmt.Errorf("clear oauth app binding: %w", err)
		}
	}

	if accountDisplayName != "" {
		if err := s.UpdateSourceDisplayName(source.ID, accountDisplayName); err != nil {
			return fmt.Errorf("set display name: %w", err)
		}
	}
	// Auto-default-identity must run BEFORE the legacy migration
	// retry — see comment on the token-reusable path above.
	if !noDefaultIdentityAddAccount {
		confirmDefaultIdentity(cmd.OutOrStdout(), s, source.ID, email, email, "account-identifier")
	}
	if err := runPostSourceCreateMigrations(s); err != nil {
		return fmt.Errorf("post-source-create migrations: %w", err)
	}

	fmt.Printf("\nAccount %s authorized successfully!\n", email)
	fmt.Println("You can now run: msgvault sync-full", email)

	return nil
}

func addAccountTokenHasGmailScopes(mgr *oauth.Manager, email string) bool {
	if !mgr.HasScopeMetadata(email) {
		return false
	}
	granted := mgr.GrantedScopes(email)
	return len(granted) == len(oauth.Scopes) && slices.Contains(granted, oauth.ScopeGmailReadonly)
}

func findGmailSource(
	s *store.Store, email string,
) (*store.Source, error) {
	sources, err := s.GetSourcesByIdentifier(email)
	if err != nil {
		return nil, fmt.Errorf("look up sources for %s: %w", email, err)
	}
	for _, src := range sources {
		if src.SourceType == sourceTypeGmail {
			return src, nil
		}
	}
	return nil, fmt.Errorf("identifier %q: %w", email, errGmailSourceNotFound)
}

func registerAddAccountFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&headless, "headless", false, "Show instructions for headless server setup")
	cmd.Flags().BoolVar(&forceReauth, "force", false, "Delete existing token and re-authorize")
	cmd.Flags().StringVar(&accountDisplayName, "display-name", "", "Display name for the account (e.g., \"Work\", \"Personal\")")
	cmd.Flags().StringVar(&oauthAppName, "oauth-app", "", "Named OAuth app from config (for Google Workspace orgs)")
	cmd.Flags().BoolVar(&noDefaultIdentityAddAccount, "no-default-identity", false, noDefaultIdentityHelp)
}

func init() {
	registerAddAccountFlags(addAccountCmd)
	rootCmd.AddCommand(newAddAccountCmd())
}
