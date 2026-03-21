package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the top-level configuration for mcp-broker.
type Config struct {
	Servers []ServerConfig `json:"servers"`
	Rules   []RuleConfig   `json:"rules"`
	Port    int            `json:"port"`
	Audit   AuditConfig    `json:"audit"`
	Log     LogConfig      `json:"log"`
}

// ServerConfig defines a backend MCP server.
type ServerConfig struct {
	Name    string            `json:"name"`
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
		Servers: []ServerConfig{},
		Rules: []RuleConfig{
			{Tool: "*", Verdict: "require-approval"},
		},
		Port: 8200,
		Audit: AuditConfig{
			Path: filepath.Join(xdgDataHome(), "mcp-broker", "audit.db"),
		},
		Log: LogConfig{Level: "info"},
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
