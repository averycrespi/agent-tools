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
	DefaultTemplate string   `json:"default_template"`
	DatabasePath    string   `json:"database_path"`
	TemplateDirs    []string `json:"template_dirs"`
}

func Default() Config {
	return Config{
		DefaultTemplate: "",
		DatabasePath:    "",
		TemplateDirs:    []string{filepath.Join(ConfigDir(), "templates")},
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
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config: %w", err)
	}
	def := Default()
	if cfg.TemplateDirs == nil {
		cfg.TemplateDirs = def.TemplateDirs
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

func (c Config) DBPath() string {
	if c.DatabasePath != "" {
		return ExpandTilde(c.DatabasePath)
	}
	return filepath.Join(StateDir(), "ad.db")
}

func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ad")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ad")
}

func ConfigFilePath() string { return filepath.Join(ConfigDir(), "config.json") }

func DataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "ad")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "ad")
}

func StateDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "ad")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "ad")
}

func RuntimeDir() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "ad")
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
