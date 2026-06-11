package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DatabasePath                 string `json:"database_path"`
	DefaultWorktreeCleanupPolicy string `json:"default_worktree_cleanup_policy"`
}

func Default() Config {
	return Config{
		DatabasePath:                 "",
		DefaultWorktreeCleanupPolicy: "never",
	}
}

func Load() (Config, error) {
	data, err := os.ReadFile(ConfigFilePath()) //nolint:gosec // config path is controlled by XDG/user home.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("failed to read config: %w", err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Refresh(logger *slog.Logger) error {
	if err := os.MkdirAll(ConfigDir(), 0o750); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = DefaultDBPath()
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := os.WriteFile(ConfigFilePath(), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	logger.Debug("refreshed config", "path", ConfigFilePath())
	return nil
}

func (c Config) Validate() error {
	switch c.DefaultWorktreeCleanupPolicy {
	case "never", "on-success", "on-terminal":
		return nil
	default:
		return fmt.Errorf("default_worktree_cleanup_policy must be one of never, on-success, or on-terminal")
	}
}

func (c Config) DBPath() string {
	if c.DatabasePath != "" {
		return ExpandTilde(c.DatabasePath)
	}
	return DefaultDBPath()
}

// DefaultDBPath is the database path used when the config does not set one.
func DefaultDBPath() string { return filepath.Join(StateDir(), "pd.db") }

func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pd")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pd")
}

func ConfigFilePath() string { return filepath.Join(ConfigDir(), "config.json") }

func DataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "pd")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "pd")
}

func StateDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pd")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pd")
}

func RuntimeDir() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "pd")
	}
	return filepath.Join(StateDir(), "run")
}

func TaskDir(taskID string) string { return filepath.Join(StateDir(), "tasks", taskID) }

func ExpandTilde(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
