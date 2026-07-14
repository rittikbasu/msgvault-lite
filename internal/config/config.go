// Package config handles loading and managing msgvault configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"go.kenn.io/msgvault/internal/fileutil"
)

// IdentityConfig holds the user's curated identity addresses.
type IdentityConfig struct {
	Addresses []string `toml:"addresses"`
}

// BackupConfig holds default settings for `msgvault backup` (spec Section
// 10). Repo lets `--repo` be omitted on every invocation; ZstdLevel tunes
// the pack compression level (0 keeps kit/pack's own default).
type BackupConfig struct {
	Repo      string `toml:"repo"`       // Default snapshot repository directory
	ZstdLevel int    `toml:"zstd_level"` // 0 (default) or 1-19
}

// Validate enforces the zstd compression level range: 0 (meaning "use
// kit/pack's default") or 1-19, matching the range the zstd encoder
// actually accepts.
func (b *BackupConfig) Validate() error {
	if b.ZstdLevel == 0 || (b.ZstdLevel >= 1 && b.ZstdLevel <= 19) {
		return nil
	}
	return fmt.Errorf("invalid [backup] zstd_level %d (want 0 or 1-19)", b.ZstdLevel)
}

type Config struct {
	Data     DataConfig     `toml:"data"`
	Log      LogConfig      `toml:"log"`
	OAuth    OAuthConfig    `toml:"oauth"`
	Sync     SyncConfig     `toml:"sync"`
	Identity IdentityConfig `toml:"identity"`
	Backup   BackupConfig   `toml:"backup"`

	// Computed paths (not from config file)
	HomeDir    string `toml:"-"`
	configPath string // resolved path to the loaded config file
}

// LogConfig holds logging configuration. File logging is opt-in:
// set enabled = true or dir = "..." to write structured JSON logs
// to disk. Without either, msgvault only writes to stderr (which
// is the default behavior users already expect). The --log-file
// CLI flag also enables file logging for a single run.
type LogConfig struct {
	// Dir is the directory where log files live. Empty means
	// "<data dir>/logs". Setting this implicitly enables file
	// logging.
	Dir string `toml:"dir"`

	// Level overrides the default logging level. Accepted values
	// are "debug", "info", "warn", "error". Empty means "info"
	// (or "debug" when --verbose is passed).
	Level string `toml:"level"`

	// Enabled turns on persistent file logging. When false (the
	// default), the CLI only writes to stderr. Set to true, or
	// set dir, to opt in to durable on-disk logs.
	Enabled bool `toml:"enabled"`

	// SQLSlowMs is the threshold above which any individual SQL
	// query is logged at WARN regardless of the main level.
	// Zero means "use the built-in default" (100 ms). Set to a
	// very large value to effectively disable slow logging.
	SQLSlowMs int64 `toml:"sql_slow_ms"`

	// SQLTrace, when true, logs every SQL query at INFO level
	// with statement text, arg count, duration, and error. This
	// is voluminous — leave off in normal use and flip it on
	// (via config or --log-sql) only when debugging.
	SQLTrace bool `toml:"sql_trace"`
}

// DataConfig holds data storage configuration.
type DataConfig struct {
	DataDir string `toml:"data_dir"`
}

// OAuthApp holds configuration for a named OAuth application.
type OAuthApp struct {
	ClientSecrets     string `toml:"client_secrets"`
	ServiceAccountKey string `toml:"service_account_key"`
}

// OAuthConfig holds OAuth configuration.
type OAuthConfig struct {
	ClientSecrets     string              `toml:"client_secrets"`
	ServiceAccountKey string              `toml:"service_account_key"`
	Apps              map[string]OAuthApp `toml:"apps"`
}

// ClientSecretsFor returns the client secrets path for the given app name.
// Empty name returns the default. Non-empty name looks up Apps[name].
func (o *OAuthConfig) ClientSecretsFor(name string) (string, error) {
	if name == "" {
		if o.ClientSecrets == "" {
			return "", errors.New("OAuth client secrets not configured.\n\n" +
				"Set [oauth] client_secrets in config.toml, or use --oauth-app <name>")
		}
		return o.ClientSecrets, nil
	}
	app, ok := o.Apps[name]
	if !ok {
		return "", fmt.Errorf("OAuth app %q not configured. Add it to config.toml:\n\n"+
			"  [oauth.apps.%s]\n"+
			"  client_secrets = \"/path/to/client_secret.json\"", name, name)
	}
	if app.ClientSecrets == "" {
		return "", fmt.Errorf("OAuth app %q has no client_secrets path configured", name)
	}
	return app.ClientSecrets, nil
}

// ServiceAccountKeyFor returns the service account key path for the given app name.
// Empty name returns the default. Non-empty name looks up Apps[name].
// Returns "" if no service account key is configured for the given app.
func (o *OAuthConfig) ServiceAccountKeyFor(name string) string {
	if name == "" {
		return o.ServiceAccountKey
	}
	if app, ok := o.Apps[name]; ok {
		return app.ServiceAccountKey
	}
	return ""
}

// HasAnyConfig returns true if any OAuth configuration exists
// (default or named apps).
func (o *OAuthConfig) HasAnyConfig() bool {
	if o.ClientSecrets != "" || o.ServiceAccountKey != "" {
		return true
	}
	for _, app := range o.Apps {
		if app.ClientSecrets != "" || app.ServiceAccountKey != "" {
			return true
		}
	}
	return false
}

// SyncConfig holds sync-related configuration.
type SyncConfig struct {
	RateLimitQPS int `toml:"rate_limit_qps"`
}

// DefaultHome returns the default msgvault home directory.
// Respects MSGVAULT_HOME environment variable and expands ~ in its value.
func DefaultHome() string {
	if h := os.Getenv("MSGVAULT_HOME"); h != "" {
		return expandPath(h)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".msgvault"
	}
	return filepath.Join(home, ".msgvault")
}

// NewDefaultConfig returns a configuration with default values.
func NewDefaultConfig() *Config {
	homeDir := DefaultHome()
	return &Config{
		HomeDir: homeDir,
		Data: DataConfig{
			DataDir: homeDir,
		},
		Sync: SyncConfig{
			RateLimitQPS: 5,
		},
	}
}

// Load reads the configuration from the specified file.
// If path is empty, uses the default location (~/.msgvault/config.toml),
// which is optional (missing file returns defaults).
// If path is explicitly provided, the file must exist.
//
// homeDir overrides the home directory (equivalent to MSGVAULT_HOME).
// When set, config.toml is loaded from homeDir unless path is also set.
func Load(path, homeDir string) (*Config, error) {
	explicit := path != ""

	cfg := NewDefaultConfig()

	// --home overrides the default home directory, just like MSGVAULT_HOME.
	if homeDir != "" {
		homeDir = expandPath(homeDir)
		cfg.HomeDir = homeDir
		cfg.Data.DataDir = homeDir
	}

	if !explicit {
		path = filepath.Join(cfg.HomeDir, "config.toml")
	} else {
		// Expand ~ for explicit paths (e.g. --config "~/.msgvault/config.toml"
		// where the shell didn't expand it, or on Windows where ~ is never expanded).
		path = expandPath(path)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if explicit {
			return nil, fmt.Errorf("config file not found: %s", path)
		}
		// Default config file is optional
		return cfg, nil
	}

	cfg.configPath = path

	// When --config points to a custom location without --home,
	// derive HomeDir and default DataDir from the config file's parent
	// directory so that tokens, database, attachments, etc. live alongside
	// the config.
	if explicit && homeDir == "" {
		cfg.HomeDir = filepath.Dir(path)
		cfg.Data.DataDir = cfg.HomeDir
	}

	metadata, err := toml.DecodeFile(path, cfg)
	if err != nil {
		if strings.Contains(err.Error(), "invalid escape") ||
			strings.Contains(err.Error(), "hexadecimal digits after") {
			return nil, fmt.Errorf("decode config: %w -- hint: Windows paths in TOML must use "+
				"forward slashes (C:/Games/msgvault) or single quotes ('C:\\Games\\msgvault')", err)
		}
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if metadata.IsDefined("data", "database_url") {
		return nil, errors.New(
			"[data].database_url is no longer supported; move the SQLite archive to <data_dir>/msgvault.db",
		)
	}

	// Expand ~ in paths
	cfg.Data.DataDir = expandPath(cfg.Data.DataDir)
	cfg.Log.Dir = expandPath(cfg.Log.Dir)
	cfg.OAuth.ClientSecrets = expandPath(cfg.OAuth.ClientSecrets)
	cfg.OAuth.ServiceAccountKey = expandPath(cfg.OAuth.ServiceAccountKey)
	cfg.Backup.Repo = expandPath(cfg.Backup.Repo)
	for name, app := range cfg.OAuth.Apps {
		app.ClientSecrets = expandPath(app.ClientSecrets)
		app.ServiceAccountKey = expandPath(app.ServiceAccountKey)
		cfg.OAuth.Apps[name] = app
	}

	// When --config is used, resolve relative paths against the config file's
	// directory so behavior doesn't depend on the working directory.
	if explicit {
		cfg.Data.DataDir = resolveRelative(cfg.Data.DataDir, cfg.HomeDir)
		cfg.Log.Dir = resolveRelative(cfg.Log.Dir, cfg.HomeDir)
		cfg.OAuth.ClientSecrets = resolveRelative(cfg.OAuth.ClientSecrets, cfg.HomeDir)
		cfg.OAuth.ServiceAccountKey = resolveRelative(cfg.OAuth.ServiceAccountKey, cfg.HomeDir)
		cfg.Backup.Repo = resolveRelative(cfg.Backup.Repo, cfg.HomeDir)
		for name, app := range cfg.OAuth.Apps {
			app.ClientSecrets = resolveRelative(app.ClientSecrets, cfg.HomeDir)
			app.ServiceAccountKey = resolveRelative(app.ServiceAccountKey, cfg.HomeDir)
			cfg.OAuth.Apps[name] = app
		}
	}

	if err := cfg.Backup.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// DatabasePath returns the local SQLite archive path.
func (c *Config) DatabasePath() string {
	return filepath.Join(c.Data.DataDir, "msgvault.db")
}

// AttachmentsDir returns the path to the attachments directory.
func (c *Config) AttachmentsDir() string {
	return filepath.Join(c.Data.DataDir, "attachments")
}

// TokensDir returns the path to the OAuth tokens directory.
func (c *Config) TokensDir() string {
	return filepath.Join(c.Data.DataDir, "tokens")
}

// LogsDir returns the path to the logs directory. Uses [log].dir
// from config when set; otherwise falls back to <data_dir>/logs.
func (c *Config) LogsDir() string {
	if c.Log.Dir != "" {
		return c.Log.Dir
	}
	return filepath.Join(c.Data.DataDir, "logs")
}

// EnsureHomeDir creates the msgvault home directory if it doesn't exist.
func (c *Config) EnsureHomeDir() error {
	return fileutil.SecureMkdirAll(c.HomeDir, 0700)
}

// ConfigFilePath returns the path to the config file.
// If a config was loaded (including via --config), returns the actual path used.
// Otherwise returns the default location based on HomeDir.
func (c *Config) ConfigFilePath() string {
	if c.configPath != "" {
		return c.configPath
	}
	return filepath.Join(c.HomeDir, "config.toml")
}

// Save writes the current configuration to disk atomically.
// Uses temp file + rename to prevent partial writes on crash.
// Enforces 0600 permissions regardless of existing file mode.
func (c *Config) Save() error {
	path := c.ConfigFilePath()

	// Resolve symlinks so atomic rename replaces the target, not
	// the symlink itself. EvalSymlinks fails on dangling symlinks
	// (target doesn't exist yet), so fall back to Readlink.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	} else if target, lErr := os.Readlink(path); lErr == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}

	// Ensure home directory exists
	if err := c.EnsureHomeDir(); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmp.Name()

	// Clean up temp file on any failure path
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0600); err != nil {
		return fmt.Errorf("set config file permissions: %w", err)
	}

	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync config file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename config file: %w", err)
	}

	success = true
	return nil
}

// MkTempDir creates a temporary directory with fallback logic for restricted
// environments (e.g. Windows where %TEMP% may be inaccessible due to
// permissions, antivirus, or group policy).
//
// It tries the following locations in order:
//  1. Each directory in preferredDirs (if any)
//  2. The system default temp directory (os.TempDir())
//  3. A "tmp" subdirectory under the msgvault home directory (~/.msgvault/tmp/)
//
// The first successful location is used. If all locations fail, the error
// from the system temp dir attempt is returned along with the final fallback error.
func MkTempDir(pattern string, preferredDirs ...string) (string, error) {
	// Try preferred directories first
	for _, base := range preferredDirs {
		if base == "" {
			continue
		}
		dir, err := os.MkdirTemp(base, pattern)
		if err == nil {
			secureTempDir(dir)
			return dir, nil
		}
	}

	// Try system temp dir
	dir, sysErr := os.MkdirTemp("", pattern)
	if sysErr == nil {
		secureTempDir(dir)
		return dir, nil
	}

	// Fallback: use ~/.msgvault/tmp/
	fallbackBase := filepath.Join(DefaultHome(), "tmp")
	if err := fileutil.SecureMkdirAll(fallbackBase, 0700); err != nil {
		return "", fmt.Errorf("create temp dir: %w (fallback also failed: %w)", sysErr, err)
	}
	dir, err := os.MkdirTemp(fallbackBase, pattern)
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w (fallback also failed: %w)", sysErr, err)
	}
	secureTempDir(dir)
	return dir, nil
}

// secureTempDir applies owner-only permissions to a temp directory created by
// os.MkdirTemp, which uses default permissions. On Windows, this also sets an
// owner-only DACL. Failures are logged but non-fatal.
func secureTempDir(dir string) {
	if err := fileutil.SecureChmod(dir, 0700); err != nil {
		slog.Warn("failed to secure temp directory permissions", "path", dir, "err", err)
	}
}

// resolveRelative makes a relative path absolute by joining it with base.
// Absolute paths and empty strings are returned unchanged.
func resolveRelative(path, base string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// expandPath expands ~ to the user's home directory.
// Only expands paths that are exactly "~" or start with "~/".
// It also strips surrounding single or double quotes, which Windows CMD
// passes through literally (unlike Unix shells which strip them).
func expandPath(path string) string {
	if path == "" {
		return path
	}
	// Strip surrounding quotes left by Windows CMD (e.g. --home 'C:\Users\foo').
	// Only on Windows — Unix shells strip quotes before the process sees them,
	// and literal quote characters in Unix paths are valid (if unusual).
	if runtime.GOOS == "windows" && len(path) >= 2 &&
		((path[0] == '\'' && path[len(path)-1] == '\'') ||
			(path[0] == '"' && path[len(path)-1] == '"')) {
		path = path[1 : len(path)-1]
	}
	if path == "~" || strings.HasPrefix(path, "~"+string(os.PathSeparator)) || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		// Trim leading slashes from the suffix to handle cases like "~//foo"
		suffix := path[2:]
		for len(suffix) > 0 && (suffix[0] == '/' || suffix[0] == os.PathSeparator) {
			suffix = suffix[1:]
		}
		return filepath.Join(home, suffix)
	}
	return path
}
