package config

import (
	"encoding/json"
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
	Rules                  []RuleConfig            `json:"rules"`
	Host                   string                  `json:"host"`
	Port                   int                     `json:"port"`
	OpenBrowser            bool                    `json:"open_browser"`
	Audit                  AuditConfig             `json:"audit"`
	Log                    LogConfig               `json:"log"`
	ApprovalTimeoutSeconds int                     `json:"approval_timeout_seconds"`
	Telegram               TelegramConfig          `json:"telegram"`
}

// ServerConfig defines a backend MCP server.
type ServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Type    string            `json:"type,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// RuleConfig defines a policy rule mapping a tool glob to a verdict.
type RuleConfig struct {
	Tool    string `json:"tool"`
	Verdict string `json:"verdict"`
}

// AuditConfig controls the SQLite audit log.
type AuditConfig struct {
	Path string `json:"path"`
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

// DefaultConfig returns a Config with all default values.
func DefaultConfig() Config {
	return Config{
		Servers: map[string]ServerConfig{},
		Rules: []RuleConfig{
			{Tool: "*", Verdict: "require-approval"},
		},
		Host:                   "127.0.0.1",
		Port:                   8200,
		OpenBrowser:            true,
		ApprovalTimeoutSeconds: 600,
		Audit: AuditConfig{
			Path: filepath.Join(xdgDataHome(), "mcp-broker", "audit.db"),
		},
		Log:      LogConfig{Level: "info"},
		Telegram: TelegramConfig{Enabled: false},
	}
}

// Load reads config from the given path.
// If the file does not exist, it writes DefaultConfig() and returns it.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()

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
	return cfg, nil
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
	cfg, err := Load(path)
	if err != nil {
		return "", err
	}
	return Save(cfg, path)
}
