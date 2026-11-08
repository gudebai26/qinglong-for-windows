package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// DefaultPort mirrors the Node project's BACK_PORT fallback.
	DefaultPort = 5700
	// DefaultBindHost mirrors BIND_HOST's default in back/config/index.ts.
	DefaultBindHost = "::"
	// DefaultLogLevel is used when neither LOG_LEVEL nor config.sh provides one.
	DefaultLogLevel = "info"
)

// Config is the merged view of data/config/config.sh, process environment and
// built-in defaults. config.sh is only parsed as KEY="value" assignments - it is
// never sourced or evaluated.
type Config struct {
	RootPath string
	DataPath string

	ConfigPath string
	DBPath     string
	SyslogPath string
	UploadPath string
	ScriptPath string
	// ScriptHistoryPath holds the Brotli archives of previous script versions.
	// It lives beside the database, never inside ScriptPath, so history files
	// cannot show up in the script tree.
	ScriptHistoryPath string
	// BakPath keeps a copy of a script that a "save as" replaces.
	BakPath  string
	LogPath  string
	RepoPath string
	// RawPath holds single-file subscription downloads (data/raw).
	RawPath string
	// SshdPath holds the per-subscription SSH keys and configs Node writes to
	// data/ssh.d and includes from ~/.ssh/config.
	SshdPath   string
	SamplePath string
	TmpPath    string
	// DepPath holds user installed Node/Python packages; it is appended to
	// NODE_PATH for every task.
	DepPath string
	// DepCachePath is data/dep_cache, the download cache the dependence page
	// can clear per runtime (config.dependenceCachePath in Node).
	DepCachePath string
	// ShellPath is the shell/ directory shipped beside the binary.
	ShellPath string
	// LangEnvFile is shell/preload/lang_env.sh, sourced by every task. The
	// language setting is mirrored there so shell scripts see QL_LANG.
	LangEnvFile string
	// VersionFile is version.yaml at the repository root.
	VersionFile string
	// DataTgzFile is the data export/import archive in the temp directory.
	DataTgzFile string

	KeyvPath string

	Port     int
	BindHost string
	// BaseURL is normalized to either "" or "/prefix" (no trailing slash).
	BaseURL     string
	LogLevel    string
	CORSOrigins []string
	TrustProxy  string

	// JWTSecret signs panel tokens. Node reads it from .env (falling back to
	// "whyour-secret"), so the Go panel must resolve it the same way or every
	// token minted by one implementation is rejected by the other.
	JWTSecret string
	// JWTExpiresIn overrides the per-login 20d/60d lifetime when set, exactly
	// like config.jwt.expiresIn in back/services/user.ts.
	JWTExpiresIn string
	// MaxTokensPerPlatform caps concurrent sessions per platform (10 in Node).
	MaxTokensPerPlatform int
	// DeployEnv mirrors process.env.DeployEnv; "demo" turns off account edits.
	DeployEnv string

	// DotEnv holds rootPath/.env, loaded with dotenv semantics: real process
	// environment wins, .env fills the gaps.
	DotEnv map[string]string

	// Values holds every assignment parsed from config.sh, with environment
	// variables applied on top. Later milestones read domain specific keys
	// (CommandTimeoutTime, RandomDelay, ...) from here.
	Values map[string]string
}

// APIWhiteList mirrors config.apiWhiteList: exact paths that never require a
// JWT. Matching is path-only, so every method is exempt (Node behaviour).
var APIWhiteList = []string{
	"/api/user/login",
	"/api/health",
	"/open/auth/token",
	"/api/user/two-factor/login",
	"/api/system",
	"/api/user/init",
	"/api/user/notification/init",
	"/open/user/login",
	"/open/user/two-factor/login",
	"/open/system",
	"/open/user/init",
	"/open/user/notification/init",
}

// BlackFileList mirrors config.blackFileList: names that may never be read or
// written through the script endpoints, even though data/config is a writable
// root. Matching is per path segment and case insensitive.
var BlackFileList = []string{
	"auth.json",
	"config.sh.sample",
	"cookie.sh",
	"crontab.list",
	"dependence-proxy.sh",
	"env.sh",
	"env.js",
	"env.py",
	"token.json",
	"grpc",
	"__pycache__",
}

// Load builds the runtime configuration. It never fails when config.sh is
// missing: the Node panel behaves the same way and falls back to defaults.
func Load() (*Config, error) {
	root, err := resolveRoot()
	if err != nil {
		return nil, err
	}

	dataPath := strings.TrimSpace(os.Getenv("QL_DATA_DIR"))
	if dataPath != "" {
		dataPath = cleanDir(dataPath)
	} else {
		dataPath = filepath.Join(root, "data")
	}

	values := map[string]string{}
	confFile := filepath.Join(dataPath, "config", "config.sh")
	switch content, err := os.ReadFile(confFile); {
	case err == nil:
		values = ParseConfigSh(string(content))
	case errors.Is(err, os.ErrNotExist):
		// First run: keep the defaults, config.sh is created by the panel later.
	default:
		return nil, fmt.Errorf("read %s: %w", confFile, err)
	}

	// Environment variables take precedence over config.sh values.
	for key := range values {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}

	port, err := resolvePort(values)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		RootPath: root,
		DataPath: dataPath,

		ConfigPath: filepath.Join(dataPath, "config"),
		DBPath:     filepath.Join(dataPath, "db"),
		KeyvPath:   filepath.Join(dataPath, "db", "keyv.sqlite"),
		SyslogPath: filepath.Join(dataPath, "syslog"),
		UploadPath: filepath.Join(dataPath, "upload"),
		ScriptPath: filepath.Join(dataPath, "scripts"),
		// back/services/scriptHistory.ts stores archives in
		// path.join(config.dataPath, 'script-history').
		ScriptHistoryPath: filepath.Join(dataPath, "script-history"),
		BakPath:           filepath.Join(dataPath, "bak"),
		LogPath:           filepath.Join(dataPath, "log"),
		RepoPath:          filepath.Join(dataPath, "repo"),
		RawPath:           filepath.Join(dataPath, "raw"),
		SshdPath:          filepath.Join(dataPath, "ssh.d"),
		SamplePath:        filepath.Join(root, "sample"),
		TmpPath:           filepath.Join(root, ".tmp"),
		DepPath:           filepath.Join(dataPath, "deps"),
		DepCachePath:      filepath.Join(dataPath, "dep_cache"),
		ShellPath:         filepath.Join(root, "shell"),
		LangEnvFile:       filepath.Join(root, "shell", "preload", "lang_env.sh"),
		VersionFile:       filepath.Join(root, "version.yaml"),
		DataTgzFile:       filepath.Join(root, ".tmp", "data.tgz"),

		Port:     port,
		BindHost: firstNonEmpty(os.Getenv("BIND_HOST"), values["BIND_HOST"], DefaultBindHost),
		BaseURL:  NormalizeBaseURL(firstNonEmpty(os.Getenv("QlBaseUrl"), values["QlBaseUrl"])),
		LogLevel: strings.ToLower(firstNonEmpty(os.Getenv("LOG_LEVEL"), values["LogLevel"], DefaultLogLevel)),
		TrustProxy: firstNonEmpty(
			os.Getenv("QL_TRUST_PROXY"),
			values["QL_TRUST_PROXY"],
			"loopback",
		),
		CORSOrigins: parseOrigins(os.Getenv("CORS_ORIGIN")),
		Values:      values,
		DotEnv:      parseDotEnv(filepath.Join(root, ".env")),
	}
	cfg.MaxTokensPerPlatform = 10
	cfg.JWTSecret = firstNonEmpty(cfg.Env("JWT_SECRET"), "whyour-secret")
	cfg.JWTExpiresIn = strings.TrimSpace(cfg.Env("JWT_EXPIRES_IN"))
	cfg.DeployEnv = cfg.Env("DeployEnv")

	return cfg, nil
}

// Env resolves a key with Node's precedence: real environment first, then the
// root .env file, then config.sh.
func (c *Config) Env(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	if c == nil {
		return ""
	}
	if value, ok := c.DotEnv[key]; ok {
		return value
	}
	return c.Values[key]
}

// IsDemo reports whether the panel runs in demo mode (DeployEnv=demo), which
// disables account edits exactly like isDemoEnv() in the Node backend.
func (c *Config) IsDemo() bool { return c.Env("DeployEnv") == "demo" }

// parseDotEnv implements the subset of dotenv the panel relies on: KEY=VALUE
// lines, optional export prefix, single/double quotes and # comments.
func parseDotEnv(path string) map[string]string {
	values := map[string]string{}
	content, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		raw = strings.TrimSpace(raw)
		if len(raw) >= 2 && (raw[0] == '"' || raw[0] == '\'') && raw[len(raw)-1] == raw[0] {
			raw = raw[1 : len(raw)-1]
		} else if idx := strings.Index(raw, " #"); idx >= 0 {
			raw = strings.TrimSpace(raw[:idx])
		}
		values[key] = raw
	}
	return values
}

// ListenAddr returns an address usable by net.Listen. Wildcard hosts are
// collapsed to ":port" so that Go binds dual-stack, matching Node's "::".
func (c *Config) ListenAddr() string {
	switch c.BindHost {
	case "", "::", "0.0.0.0", "*":
		return ":" + strconv.Itoa(c.Port)
	default:
		return net.JoinHostPort(c.BindHost, strconv.Itoa(c.Port))
	}
}

// Get returns a config value with the environment taking precedence over
// config.sh, or "" when neither defines it.
func (c *Config) Get(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	if c == nil || c.Values == nil {
		return ""
	}
	return c.Values[key]
}

// DependenceProxyFile is data/config/dependence-proxy.sh. When it exists the
// dependency installer sources it first, which is how a mirror is configured.
func (c *Config) DependenceProxyFile() string {
	return filepath.Join(c.ConfigPath, "dependence-proxy.sh")
}

// GetDefault returns a config value or the supplied fallback.
func (c *Config) GetDefault(key, fallback string) string {
	if value := c.Get(key); value != "" {
		return value
	}
	return fallback
}

// EnvDefault is GetDefault with Node's precedence (environment, .env,
// config.sh). The Node panel reads GRPC_PORT/BIND_HOST_GRPC straight from
// process.env, and dotenv fills that from .env, so those keys must honour the
// file too.
func (c *Config) EnvDefault(key, fallback string) string {
	if value := strings.TrimSpace(c.Env(key)); value != "" {
		return value
	}
	return fallback
}

// EnvInt parses an integer with EnvDefault precedence.
func (c *Config) EnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(c.Env(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// GetBool interprets a config.sh value the way the shell scripts do: only
// "true"/"1"/"yes" (case insensitive) count as enabled.
func (c *Config) GetBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(c.Get(key))) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// GetInt parses an integer config.sh value, returning fallback when empty or
// malformed.
func (c *Config) GetInt(key string, fallback int) int {
	raw := strings.TrimSpace(c.Get(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// EnsureDirs creates the data directories the HTTP layer needs. Existing
// directories are left untouched. The set matches back/loaders/initFile.ts;
// the history directory is private because it holds script contents.
func (c *Config) EnsureDirs() error {
	for _, dir := range []string{
		c.DataPath, c.ConfigPath, c.DBPath, c.SyslogPath, c.UploadPath,
		c.ScriptPath, c.BakPath, c.TmpPath, c.RepoPath, c.RawPath,
		c.DepPath, c.SshdPath, c.DepCachePath,
		// Subscription logs live one directory per alias below this root; the
		// directory itself is created by the first run, exactly like Node (an
		// always-present empty "subscription" folder would show up in /api/logs).
		filepath.Join(c.LogPath, ".tmp"),
	} {
		if dir == "" {
			// Tests build a partial Config; an unset directory is not an error.
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if c.ScriptHistoryPath == "" {
		return nil
	}
	if err := os.MkdirAll(c.ScriptHistoryPath, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", c.ScriptHistoryPath, err)
	}
	return nil
}

// NormalizeBaseURL mirrors back/config/index.ts: a leading slash is added and
// the trailing slash removed.
func NormalizeBaseURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return strings.TrimRight(value, "/")
}

func resolvePort(values map[string]string) (int, error) {
	for _, key := range []string{"BACK_PORT", "QL_PORT"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			raw = strings.TrimSpace(values[key])
		}
		if raw == "" {
			continue
		}
		port, err := strconv.Atoi(raw)
		if err != nil || port <= 0 || port > 65535 {
			return 0, fmt.Errorf("invalid %s %q", key, raw)
		}
		return port, nil
	}
	return DefaultPort, nil
}

func parseOrigins(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{"*"}
	}
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	if len(origins) == 0 {
		return []string{"*"}
	}
	return origins
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// EnsureConfigFile creates data/config/config.sh from the sample on a fresh
// install, the way back/loaders/initFile.ts does. An existing file is never
// touched: it holds every panel setting the operator edited.
func (c *Config) EnsureConfigFile(sample []byte) error {
	target := filepath.Join(c.ConfigPath, "config.sh")
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if len(sample) == 0 {
		return nil
	}
	if err := os.MkdirAll(c.ConfigPath, 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, sample, 0o644)
}
