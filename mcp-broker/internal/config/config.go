package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the top-level configuration for mcp-broker.
//
// Host must resolve to a loopback interface — startup rejects anything else.
// The broker is protected only by a bearer token over plain HTTP; its
// security posture relies on not being network-reachable.
type Config struct {
	Servers                map[string]ServerConfig `json:"servers"`
	RulesPath              string                  `json:"rules_path"`
	ToolPatches            []ToolPatchConfig       `json:"tool_patches,omitempty"`
	Host                   string                  `json:"host"`
	Port                   int                     `json:"port"`
	OpenBrowser            bool                    `json:"open_browser"`
	Audit                  AuditConfig             `json:"audit"`
	Grants                 GrantsConfig            `json:"grants"`
	Log                    LogConfig               `json:"log"`
	ApprovalTimeoutSeconds int                     `json:"approval_timeout_seconds"`
	MaxRequestBodyBytes    int64                   `json:"max_request_body_bytes"`
	Telegram               TelegramConfig          `json:"telegram"`
}

// MaxStartupRetryCount bounds configured startup retries to avoid overflow and accidental long startup delays.
const MaxStartupRetryCount = 1000

// ServerConfig defines a backend MCP server.
type ServerConfig struct {
	Command               string            `json:"command,omitempty"`
	Args                  []string          `json:"args,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	Type                  string            `json:"type,omitempty"`
	URL                   string            `json:"url,omitempty"`
	Headers               map[string]string `json:"headers,omitempty"`
	OAuth                 *OAuthConfig      `json:"oauth,omitempty"`
	HTTPTimeoutSeconds    int               `json:"http_timeout_seconds,omitempty"`
	StartupRetryCount     *int              `json:"startup_retry_count,omitempty"`
	StartupRetryBackoffMS *int              `json:"startup_retry_backoff_ms,omitempty"`
	StartupTimeoutSeconds *int              `json:"startup_timeout_seconds,omitempty"`
}

// OAuthConfig configures an OAuth client for HTTP/SSE backends that do not
// support dynamic client registration, or that require a fixed redirect port.
type OAuthConfig struct {
	ClientID              string   `json:"client_id,omitempty"`
	ClientSecret          string   `json:"client_secret,omitempty"`
	CallbackPort          int      `json:"callback_port,omitempty"`
	Scopes                []string `json:"scopes,omitempty"`
	AuthServerMetadataURL string   `json:"auth_server_metadata_url,omitempty"`
}

// RuleConfig defines a policy rule mapping a tool glob to a verdict.
// Args, when non-empty, additionally constrains the rule to tool calls whose
// arguments satisfy every pattern.
type RuleConfig struct {
	Tool    string       `json:"tool"`
	Verdict string       `json:"verdict"`
	Reason  string       `json:"reason,omitempty"`
	Args    []ArgPattern `json:"args,omitempty"`
}

// ToolPatchConfig defines a load-time transform for a discovered tool.
type ToolPatchConfig struct {
	Tool        string                `json:"tool"`
	Disabled    bool                  `json:"disabled,omitempty"`
	Annotations *ToolAnnotationsPatch `json:"annotations,omitempty"`
}

func (p *ToolPatchConfig) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Tool          string                `json:"tool"`
		Disabled      *bool                 `json:"disabled"`
		LegacyDisable *bool                 `json:"disable"`
		Annotations   *ToolAnnotationsPatch `json:"annotations"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	p.Tool = decoded.Tool
	p.Annotations = decoded.Annotations
	switch {
	case decoded.Disabled != nil:
		p.Disabled = *decoded.Disabled
	case decoded.LegacyDisable != nil:
		p.Disabled = *decoded.LegacyDisable
	default:
		p.Disabled = false
	}
	return nil
}

// ToolAnnotationsPatch defines field-level overrides for MCP tool annotations.
type ToolAnnotationsPatch struct {
	Title           *string `json:"title,omitempty"`
	ReadOnlyHint    *bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool   `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool   `json:"openWorldHint,omitempty"`
}

// ArgPattern constrains a rule to tool calls where the value at Path matches.
// Match is either a JSON string (exact match) or {"regex": "<RE2>"}.
// It stays as RawMessage so the config package does not depend on the rules
// package; structural and regex validation happen in rules.New.
type ArgPattern struct {
	Path  string          `json:"path"`
	Match json.RawMessage `json:"match"`
}

// AuditConfig controls the SQLite audit log.
type AuditConfig struct {
	Path string `json:"path"`
}

// GrantsConfig controls durable grant authorization state.
type GrantsConfig struct {
	Path          string `json:"path"`
	MaxTTLSeconds int64  `json:"max_ttl_seconds"`
}

// LogConfig controls logging behavior.
type LogConfig struct {
	Level string `json:"level"`
}

// TelegramConfig configures the optional Telegram approval notifier.
// Token and ChatID support $VAR / ${VAR} environment variable expansion.
type TelegramConfig struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
	ChatID  string `json:"chat_id"`
}

func xdgConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func xdgDataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// ConfigPath returns the default config file path.
func ConfigPath() string {
	return filepath.Join(xdgConfigHome(), "mcp-broker", "config.json")
}

// DefaultRules returns the default fail-closed policy rules.
func DefaultRules() []RuleConfig {
	return []RuleConfig{{Tool: "*", Verdict: "require-approval"}}
}

// DefaultConfig returns a Config with all default values.
func DefaultConfig() Config {
	return DefaultConfigAt(ConfigPath())
}

// DefaultConfigAt returns a Config with defaults resolved relative to path.
func DefaultConfigAt(path string) Config {
	return Config{
		Servers:                map[string]ServerConfig{},
		RulesPath:              filepath.Join(filepath.Dir(path), "rules.json"),
		Host:                   "127.0.0.1",
		Port:                   8200,
		OpenBrowser:            true,
		ApprovalTimeoutSeconds: 600,
		MaxRequestBodyBytes:    10 * 1024 * 1024,
		Audit: AuditConfig{
			Path: filepath.Join(xdgDataHome(), "mcp-broker", "audit.db"),
		},
		Grants: GrantsConfig{
			Path:          filepath.Join(xdgDataHome(), "mcp-broker", "grants.db"),
			MaxTTLSeconds: 7 * 24 * 60 * 60,
		},
		Log:      LogConfig{Level: "info"},
		Telegram: TelegramConfig{Enabled: false},
	}
}

// Load reads config from the given path.
// If the file does not exist, it writes DefaultConfig() and returns it.
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
		return cfg, err
	}
	if cfg.RulesPath == "" {
		cfg.RulesPath = filepath.Join(filepath.Dir(path), "rules.json")
	}
	if err := validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func validate(cfg Config) error {
	if cfg.Grants.MaxTTLSeconds <= 0 {
		return fmt.Errorf("grants.max_ttl_seconds must be positive")
	}
	for name, srv := range cfg.Servers {
		if srv.StartupRetryCount != nil && *srv.StartupRetryCount < 0 {
			return fmt.Errorf("servers.%s.startup_retry_count must be non-negative", name)
		}
		if srv.StartupRetryCount != nil && *srv.StartupRetryCount > MaxStartupRetryCount {
			return fmt.Errorf("servers.%s.startup_retry_count must be <= %d", name, MaxStartupRetryCount)
		}
		if srv.StartupRetryBackoffMS != nil && *srv.StartupRetryBackoffMS < 0 {
			return fmt.Errorf("servers.%s.startup_retry_backoff_ms must be non-negative", name)
		}
		if srv.StartupTimeoutSeconds != nil && *srv.StartupTimeoutSeconds < 0 {
			return fmt.Errorf("servers.%s.startup_timeout_seconds must be non-negative", name)
		}
		if srv.OAuth != nil && (srv.OAuth.CallbackPort < 0 || srv.OAuth.CallbackPort > 65535) {
			return fmt.Errorf("servers.%s.oauth.callback_port must be between 0 and 65535", name)
		}
	}
	return nil
}

// Save writes cfg to path. Creates parent directories as needed.
// Returns the path written.
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

// Refresh loads the config (with defaults overlay), then writes it back.
// This fills in any new default values. Returns the path written.
func Refresh(path string) (string, error) {
	written, _, err := RefreshWithResult(path)
	return written, err
}

// RefreshWithResult refreshes config and rules and returns rules source metadata.
func RefreshWithResult(path string) (string, RulesLoadResult, error) {
	cfg, err := Load(path)
	if err != nil {
		return "", RulesLoadResult{}, err
	}
	result, err := LoadRulesForConfig(path, cfg)
	if err != nil {
		return "", result, err
	}
	written, err := Save(cfg, path)
	if err != nil {
		return "", result, err
	}
	return written, result, nil
}
