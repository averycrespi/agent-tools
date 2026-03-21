package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Config represents the sb configuration file.
type Config struct {
	Image     string   `json:"image"`
	CPUs      int      `json:"cpus"`
	Memory    string   `json:"memory"`
	Disk      string   `json:"disk"`
	Mounts    []string `json:"mounts"`
	CopyPaths []string `json:"copy_paths"`
	Scripts   []string `json:"scripts"`
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		Image:     "ubuntu-24.04",
		CPUs:      4,
		Memory:    "4GiB",
		Disk:      "100GiB",
		Mounts:    []string{},
		CopyPaths: []string{},
		Scripts:   []string{},
	}
}

// Load reads the config from disk. Returns Default() if the file doesn't exist.
func Load() (Config, error) {
	path := ConfigFilePath()
	data, err := os.ReadFile(path) //nolint:gosec // path is from ConfigFilePath(), not user input
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("failed to read config %q: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config %q: %w", path, err)
	}

	// Ensure nil slices become empty slices.
	if cfg.Mounts == nil {
		cfg.Mounts = []string{}
	}
	if cfg.CopyPaths == nil {
		cfg.CopyPaths = []string{}
	}
	if cfg.Scripts == nil {
		cfg.Scripts = []string{}
	}

	return cfg, nil
}

// Refresh creates or updates the config file with default values for any missing fields.
func Refresh(logger *slog.Logger) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("failed to create config dir %q: %w", dir, err)
	}

	cfg, err := Load()
	if err != nil {
		cfg = Default()
	}

	// Backfill defaults for zero-value fields.
	def := Default()
	if cfg.Image == "" {
		cfg.Image = def.Image
	}
	if cfg.CPUs == 0 {
		cfg.CPUs = def.CPUs
	}
	if cfg.Memory == "" {
		cfg.Memory = def.Memory
	}
	if cfg.Disk == "" {
		cfg.Disk = def.Disk
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	path := ConfigFilePath()
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("failed to write config %q: %w", path, err)
	}

	logger.Debug("refreshed config", "path", path)
	return nil
}

// ParseCopyPath splits a copy_paths entry into source and destination,
// expanding leading "~/" to the user's home directory on both sides.
// Format: "src:dst" or "src" (dst defaults to src).
func ParseCopyPath(entry string) (src, dst string, err error) {
	parts := strings.SplitN(entry, ":", 2)
	if len(parts) == 2 {
		src, dst = parts[0], parts[1]
	} else {
		src, dst = entry, entry
	}

	src, err = expandTilde(src)
	if err != nil {
		return "", "", err
	}
	dst, err = expandTilde(dst)
	if err != nil {
		return "", "", err
	}

	// Clean trailing slashes so callers get canonical paths.
	src = strings.TrimRight(src, "/")
	dst = strings.TrimRight(dst, "/")

	return src, dst, nil
}

// expandTilde replaces a leading "~/" with the user's home directory.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to expand ~: %w", err)
	}
	return filepath.Join(home, path[2:]), nil
}

// ConfigFilePath returns the path to the config file.
func ConfigFilePath() string {
	return filepath.Join(configDir(), "config.json")
}

func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "sb")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "sb")
}
