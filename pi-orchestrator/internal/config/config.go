package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	WorkflowDir       string
	StateDir          string
	RuntimeDir        string
	ArtifactParentDir string
}

func Load() (Config, error) {
	stateDir := filepath.Join(xdgStateHome(), "po")
	workflowDir := filepath.Join(xdgConfigHome(), "po", "workflows")
	if override := os.Getenv("PO_WORKFLOW_DIR"); override != "" {
		workflowDir = override
	}
	artifactParentDir := filepath.Join(stateDir, "artifacts")
	if override := os.Getenv("PO_ARTIFACT_PARENT_DIR"); override != "" {
		artifactParentDir = override
	}
	return Config{
		WorkflowDir:       workflowDir,
		StateDir:          stateDir,
		RuntimeDir:        filepath.Join(xdgRuntimeDir(), "po"),
		ArtifactParentDir: artifactParentDir,
	}, nil
}

func (c Config) DBPath() string {
	return filepath.Join(c.StateDir, "po.db")
}

func (c Config) RunLogDir(runID string) string {
	return filepath.Join(c.StateDir, "runs", runID)
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
