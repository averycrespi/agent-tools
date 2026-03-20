package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
)

var nonAlphanumericDash = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// Config represents the wt configuration file.
type Config struct {
	LaunchCommand string   `json:"launch_command"`
	SetupScripts  []string `json:"setup_scripts"`
	CopyFiles     []string `json:"copy_files"`
}

// Default returns a Config populated with default values.
func Default() Config {
	return Config{
		LaunchCommand: "",
		SetupScripts:  []string{},
		CopyFiles:     []string{},
	}
}

// Refresh loads the config file (creating it with defaults if missing),
// then writes it back. This ensures new fields added to the schema
// appear in existing config files with their default values.
func Refresh(logger *slog.Logger) error {
	path := ConfigFilePath()

	if err := os.MkdirAll(ConfigDir(), 0o755); err != nil { //nolint:gosec // 0755 is appropriate for config directory
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	cfg, err := Load()
	if err != nil {
		return err
	}

	// Apply defaults for nil slice fields.
	if cfg.SetupScripts == nil {
		cfg.SetupScripts = []string{}
	}
	if cfg.CopyFiles == nil {
		cfg.CopyFiles = []string{}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // 0644 is appropriate for user config file
		return fmt.Errorf("failed to write config file: %w", err)
	}

	logger.Info("refreshed config file", "path", path)
	return nil
}

// Load reads and parses the config file. Returns a zero-value Config if the file doesn't exist.
func Load() (Config, error) {
	data, err := os.ReadFile(ConfigFilePath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("failed to read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config: %w", err)
	}
	return cfg, nil
}

// --- Path functions (adapted from CCO internal/paths) ---

// DataDir returns the base wt data directory.
// Uses $XDG_DATA_HOME/wt or defaults to ~/.local/share/wt.
func DataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "wt")
}

// WorktreeDir returns the full path to a workspace's worktree directory.
func WorktreeDir(repo, branch string) string {
	return filepath.Join(DataDir(), "worktrees", repo, repo+"-"+SanitizeBranch(branch))
}

// SanitizeBranch replaces non-alphanumeric characters (except hyphens) with hyphens.
func SanitizeBranch(branch string) string {
	return nonAlphanumericDash.ReplaceAllString(branch, "-")
}

// TmuxSessionName returns the tmux session name for a repository.
func TmuxSessionName(repo string) string {
	return "wt-" + repo
}

// TmuxWindowName returns the tmux window name for a branch.
func TmuxWindowName(branch string) string {
	return SanitizeBranch(branch)
}

// ConfigDir returns the wt config directory.
// Uses $XDG_CONFIG_HOME/wt or defaults to ~/.config/wt.
func ConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "wt")
}

// ConfigFilePath returns the path to the wt config file.
func ConfigFilePath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// WorktreeBaseDir returns the base directory for all worktrees.
func WorktreeBaseDir() string {
	return filepath.Join(DataDir(), "worktrees")
}
