// Package config loads and persists egress-broker's JSON configuration, and
// orchestrates loading of the separate rules document.
//
// The split mirrors mcp-broker: this package owns config.json and the
// mechanics of reading rules.json, while the rule schema and matching live in
// internal/rules.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/egress-broker/internal/paths"
)

// Default listener ports. The proxy and the dashboard get separate ports on
// purpose: a forward proxy is not an ordinary HTTP server, and sharing a port
// would make the dashboard reachable *through* the proxy.
const (
	DefaultProxyPort     = 8220
	DefaultDashboardPort = 8221
)

// DefaultRetentionDays is how long audit rows are kept before the prune loop
// removes them.
const DefaultRetentionDays = 90

// ErrNonLoopback is returned when a listener is configured on an address that
// is not loopback. The network boundary is the load-bearing control here; the
// bearer token is defence in depth.
var ErrNonLoopback = errors.New("listen address must be loopback")

// Config is the top-level configuration for egress-broker.
type Config struct {
	Proxy          ListenerConfig           `json:"proxy"`
	Dashboard      ListenerConfig           `json:"dashboard"`
	RulesPath      string                   `json:"rules_path"`
	Audit          AuditConfig              `json:"audit"`
	Log            LogConfig                `json:"log"`
	OpenBrowser    bool                     `json:"open_browser"`
	EnvCredentials map[string]EnvCredential `json:"env_credentials,omitempty"`
}

// ListenerConfig is one loopback listener.
type ListenerConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Addr returns the host:port form for net.Listen.
func (l ListenerConfig) Addr() string {
	return net.JoinHostPort(l.Host, fmt.Sprint(l.Port))
}

// AuditConfig controls the SQLite audit log.
type AuditConfig struct {
	Path          string `json:"path"`
	RetentionDays int    `json:"retention_days"`
}

// LogConfig controls logging behaviour.
type LogConfig struct {
	Level string `json:"level"`
}

// EnvCredential sources a credential value from a process environment
// variable instead of the OS keychain.
//
// It carries bound Hosts exactly as a keychain credential does, and goes
// through the identical host check. An earlier design let rules reference
// environment values by a separate ${env.*} form, which silently exempted them
// from host binding; the single enforcement path exists to make that class of
// gap impossible rather than merely documented (AC-9).
type EnvCredential struct {
	Var   string   `json:"var"`
	Hosts []string `json:"hosts"`
}

// DefaultConfig returns a Config with all default values.
func DefaultConfig() Config { return DefaultConfigAt(paths.ConfigFile()) }

// DefaultConfigAt returns a Config with defaults resolved relative to path.
func DefaultConfigAt(path string) Config {
	return Config{
		Proxy:       ListenerConfig{Host: "127.0.0.1", Port: DefaultProxyPort},
		Dashboard:   ListenerConfig{Host: "127.0.0.1", Port: DefaultDashboardPort},
		RulesPath:   filepath.Join(filepath.Dir(path), "rules.json"),
		Audit:       AuditConfig{Path: paths.AuditDB(), RetentionDays: DefaultRetentionDays},
		Log:         LogConfig{Level: "info"},
		OpenBrowser: true,
	}
}

// Load reads config from path. If the file does not exist, it writes
// DefaultConfig() and returns it.
//
// Missing scalar fields keep their default values, because the zero value is
// decoded over a Config that already holds defaults.
func Load(path string) (Config, error) {
	cfg := DefaultConfigAt(path)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if _, err := Save(cfg, path); err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing %s: %w", path, describeJSONError(data, err))
	}

	backfill(&cfg, path)
	if err := Validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// backfill restores defaults for fields a config file left empty. Without it,
// a hand-edited file that omits a port would try to bind port 0.
func backfill(cfg *Config, path string) {
	defaults := DefaultConfigAt(path)

	if cfg.Proxy.Host == "" {
		cfg.Proxy.Host = defaults.Proxy.Host
	}
	if cfg.Proxy.Port == 0 {
		cfg.Proxy.Port = defaults.Proxy.Port
	}
	if cfg.Dashboard.Host == "" {
		cfg.Dashboard.Host = defaults.Dashboard.Host
	}
	if cfg.Dashboard.Port == 0 {
		cfg.Dashboard.Port = defaults.Dashboard.Port
	}
	if cfg.RulesPath == "" {
		cfg.RulesPath = defaults.RulesPath
	}
	if cfg.Audit.Path == "" {
		cfg.Audit.Path = defaults.Audit.Path
	}
	if cfg.Audit.RetentionDays == 0 {
		cfg.Audit.RetentionDays = defaults.Audit.RetentionDays
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = defaults.Log.Level
	}
}

// Validate rejects a configuration that cannot safely serve traffic.
func Validate(cfg Config) error {
	if err := ValidateLoopback(cfg.Proxy); err != nil {
		return fmt.Errorf("proxy listener: %w", err)
	}
	if err := ValidateLoopback(cfg.Dashboard); err != nil {
		return fmt.Errorf("dashboard listener: %w", err)
	}
	if cfg.Proxy.Port == cfg.Dashboard.Port && cfg.Proxy.Host == cfg.Dashboard.Host {
		return fmt.Errorf("proxy and dashboard cannot share %s: the dashboard would be reachable through the proxy", cfg.Proxy.Addr())
	}
	if cfg.Audit.RetentionDays < 0 {
		return fmt.Errorf("audit.retention_days must not be negative")
	}
	for name, ec := range cfg.EnvCredentials {
		if ec.Var == "" {
			return fmt.Errorf("env_credentials.%s: %q is required", name, "var")
		}
		if len(ec.Hosts) == 0 {
			return fmt.Errorf("env_credentials.%s: at least one bound host is required; every credential carries host scope", name)
		}
	}
	return nil
}

// ValidateLoopback rejects any listener address that is not loopback.
//
// Sandboxed agents reach the host through Lima's user-mode forwarding of
// host.lima.internal to host loopback, so a loopback bind is sufficient. A
// non-loopback bind would expose a credential-injecting proxy to the local
// network, which no bearer token makes acceptable.
func ValidateLoopback(l ListenerConfig) error {
	host := strings.TrimSpace(l.Host)
	if host == "" {
		return fmt.Errorf("%w: host is empty", ErrNonLoopback)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return fmt.Errorf("%w: %q is not an IP address or \"localhost\"", ErrNonLoopback, l.Host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("%w: %q resolves to a non-loopback address", ErrNonLoopback, l.Host)
	}
	if l.Port < 0 || l.Port > 65535 {
		return fmt.Errorf("port %d out of range 0-65535", l.Port)
	}
	return nil
}

// Save writes cfg to path, creating parent directories as needed.
func Save(cfg Config, path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Refresh loads the config with the defaults overlay and writes it back,
// filling in any newly added default values. Returns the path written.
func Refresh(path string) (string, error) {
	cfg, err := Load(path)
	if err != nil {
		return "", err
	}
	return Save(cfg, path)
}

// describeJSONError converts a JSON syntax or type error into a message
// naming the byte offset, which AC-6 requires for file-level parse failures.
func describeJSONError(data []byte, err error) error {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		line, col := lineCol(data, syntaxErr.Offset)
		return fmt.Errorf("%w (at byte offset %d, line %d column %d)", err, syntaxErr.Offset, line, col)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		line, col := lineCol(data, typeErr.Offset)
		return fmt.Errorf("%w (at byte offset %d, line %d column %d)", err, typeErr.Offset, line, col)
	}
	return err
}

// lineCol converts a byte offset into a 1-based line and column.
func lineCol(data []byte, offset int64) (int, int) {
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	line, col := 1, 1
	for i := int64(0); i < offset; i++ {
		if data[i] == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return line, col
}
