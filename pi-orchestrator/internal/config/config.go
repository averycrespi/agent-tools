package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ConfigDir         string `json:"-"`
	WorkflowDir       string `json:"workflow_dir"`
	StateDir          string `json:"-"`
	RuntimeDir        string `json:"-"`
	ArtifactParentDir string `json:"artifact_parent_dir"`
	DatabasePath      string `json:"database_path"`
}

type fileConfig struct {
	DatabasePath      string `json:"database_path"`
	WorkflowDir       string `json:"workflow_dir"`
	ArtifactParentDir string `json:"artifact_parent_dir"`
}

func Default() Config {
	return Config{
		WorkflowDir:       DefaultWorkflowDir(),
		ArtifactParentDir: DefaultArtifactParentDir(),
		DatabasePath:      DefaultDBPath(),
	}
}

func Load() (Config, error) {
	cfg, err := loadFileConfig()
	if err != nil {
		return Config{}, err
	}
	cfg.WorkflowDir = ExpandTilde(cfg.WorkflowDir)
	cfg.ArtifactParentDir = ExpandTilde(cfg.ArtifactParentDir)
	cfg.DatabasePath = ExpandTilde(cfg.DatabasePath)
	if override := os.Getenv("PO_WORKFLOW_DIR"); override != "" {
		cfg.WorkflowDir = override
	}
	if override := os.Getenv("PO_ARTIFACT_PARENT_DIR"); override != "" {
		cfg.ArtifactParentDir = override
	}
	return cfg, nil
}

func Refresh() error {
	if err := os.MkdirAll(ConfigDir(), 0o750); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}
	cfg, err := loadFileConfig()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(fileConfig{
		DatabasePath:      cfg.DatabasePath,
		WorkflowDir:       cfg.WorkflowDir,
		ArtifactParentDir: cfg.ArtifactParentDir,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := os.WriteFile(ConfigFilePath(), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	return nil
}

func loadFileConfig() (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(ConfigFilePath()) //nolint:gosec // config path is controlled by XDG/user home.
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return Config{}, fmt.Errorf("failed to read config: %w", err)
		}
	} else if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config: %w", err)
	}
	cfg.ConfigDir = ConfigDir()
	cfg.StateDir = StateDir()
	cfg.RuntimeDir = RuntimeDir()
	cfg.WorkflowDir = defaultIfEmpty(cfg.WorkflowDir, DefaultWorkflowDir())
	cfg.ArtifactParentDir = defaultIfEmpty(cfg.ArtifactParentDir, DefaultArtifactParentDir())
	cfg.DatabasePath = defaultIfEmpty(cfg.DatabasePath, DefaultDBPath())
	return cfg, nil
}

func (c Config) DBPath() string {
	return ExpandTilde(defaultIfEmpty(c.DatabasePath, DefaultDBPath()))
}

func (c Config) RunLogDir(runID string) string {
	return filepath.Join(c.StateDir, "runs", runID)
}

func ConfigDir() string {
	return filepath.Join(xdgConfigHome(), "po")
}

func ConfigFilePath() string { return filepath.Join(ConfigDir(), "config.json") }

func StateDir() string {
	return filepath.Join(xdgStateHome(), "po")
}

func RuntimeDir() string {
	return filepath.Join(xdgRuntimeDir(), "po")
}

func DefaultWorkflowDir() string { return filepath.Join(ConfigDir(), "workflows") }

func DefaultArtifactParentDir() string { return filepath.Join(StateDir(), "artifacts") }

func DefaultDBPath() string { return filepath.Join(StateDir(), "po.db") }

func ExpandTilde(path string) string {
	if path == "~" {
		return homeDir()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}

func xdgConfigHome() string {
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		return value
	}
	return filepath.Join(homeDir(), ".config")
}

func xdgStateHome() string {
	if value := os.Getenv("XDG_STATE_HOME"); value != "" {
		return value
	}
	return filepath.Join(homeDir(), ".local", "state")
}

func xdgRuntimeDir() string {
	if value := os.Getenv("XDG_RUNTIME_DIR"); value != "" {
		return value
	}
	return filepath.Join(os.TempDir(), "po-runtime")
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}

func defaultIfEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
