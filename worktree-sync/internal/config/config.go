package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

const Version = 1

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type Paths struct {
	Config    string
	Worktrees string
	State     string
}

type Global struct {
	ReconcileInterval string `json:"reconcile_interval"`
	Debounce          string `json:"debounce"`
	CommandTimeout    string `json:"command_timeout"`
}

type Policy struct {
	SetupExplicit  bool `json:"setup_explicit"`
	LaunchExplicit bool `json:"launch_explicit"`
	SetupPassive   bool `json:"setup_passive"`
	LaunchPassive  bool `json:"launch_passive"`
}

type CopyAction struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type SetupAction struct {
	Argv    []string          `json:"argv"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
}

type Repository struct {
	ID                 string        `json:"id"`
	PrimaryRoot        string        `json:"primary_root"`
	CommonGitDir       string        `json:"common_git_dir"`
	AllowedRoots       []string      `json:"allowed_worktree_roots"`
	LaunchCommand      string        `json:"launch_command,omitempty"`
	CopyActions        []CopyAction  `json:"copy_actions,omitempty"`
	SetupActions       []SetupAction `json:"setup_actions,omitempty"`
	Policy             Policy        `json:"policy"`
	RepositoryIdentity string        `json:"repository_identity,omitempty"`
}

type Config struct {
	Version      int          `json:"version"`
	Global       Global       `json:"global"`
	Repositories []Repository `json:"repositories"`
}

func Default() Config {
	return Config{Version: Version, Global: Global{ReconcileInterval: "30s", Debounce: "250ms", CommandTimeout: "20s"}, Repositories: []Repository{}}
}

func envHome(name, fallback string) (string, error) {
	if value := os.Getenv(name); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, fallback), nil
}

func PathsFromEnv() (Paths, error) {
	configHome, err := envHome("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return Paths{}, err
	}
	dataHome, err := envHome("XDG_DATA_HOME", filepath.Join(".local", "share"))
	if err != nil {
		return Paths{}, err
	}
	stateHome, err := envHome("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return Paths{}, err
	}
	return Paths{Config: filepath.Join(configHome, "worktree-sync", "config.json"), Worktrees: filepath.Join(dataHome, "worktree-sync", "worktrees"), State: filepath.Join(stateHome, "worktree-sync")}, nil
}

func ValidID(id string) bool { return validID.MatchString(id) }

func CanonicalExisting(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("making path absolute: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("canonicalizing %s: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stating %s: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

func Contains(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func parsePositive(name, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fmt.Errorf("%s must be a positive duration", name)
	}
	return nil
}

func (c Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if err := parsePositive("reconcile_interval", c.Global.ReconcileInterval); err != nil {
		return err
	}
	if err := parsePositive("debounce", c.Global.Debounce); err != nil {
		return err
	}
	if err := parsePositive("command_timeout", c.Global.CommandTimeout); err != nil {
		return err
	}
	ids := make(map[string]bool)
	identities := make(map[string]bool)
	for _, repo := range c.Repositories {
		if !ValidID(repo.ID) || len(repo.ID) > 64 {
			return fmt.Errorf("repository ID %q must match %s and be at most 64 characters", repo.ID, validID)
		}
		if ids[repo.ID] {
			return fmt.Errorf("duplicate repository ID %q", repo.ID)
		}
		ids[repo.ID] = true
		identity := repo.RepositoryIdentity
		if identity == "" {
			identity = repo.CommonGitDir
		}
		if identities[identity] {
			return fmt.Errorf("duplicate repository identity %q", identity)
		}
		identities[identity] = true
	}
	for _, repo := range c.Repositories {
		if repo.PrimaryRoot == "" || repo.CommonGitDir == "" || len(repo.AllowedRoots) == 0 {
			return fmt.Errorf("repository %q requires primary_root, common_git_dir, and allowed roots", repo.ID)
		}
		for _, path := range append([]string{repo.PrimaryRoot, repo.CommonGitDir}, repo.AllowedRoots...) {
			canonical, err := CanonicalExisting(path)
			if err != nil {
				return fmt.Errorf("repository %q: %w", repo.ID, err)
			}
			if canonical != filepath.Clean(path) {
				return fmt.Errorf("repository %q path %q is not canonical", repo.ID, path)
			}
		}
		for _, action := range repo.CopyActions {
			for _, path := range []string{action.Source, action.Destination} {
				clean := filepath.Clean(path)
				if path == "" || filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
					return fmt.Errorf("repository %q copy paths must be non-empty, relative, and contained", repo.ID)
				}
			}
		}
		for _, action := range repo.SetupActions {
			if len(action.Argv) == 0 || action.Argv[0] == "" {
				return fmt.Errorf("repository %q setup argv must not be empty", repo.ID)
			}
			if action.Timeout != "" {
				if err := parsePositive("setup timeout", action.Timeout); err != nil {
					return err
				}
			}
			for key := range action.Env {
				if key == "" || strings.Contains(key, "=") {
					return fmt.Errorf("repository %q setup environment key %q is invalid", repo.ID, key)
				}
			}
		}
	}
	return nil
}

func decode(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller supplies the XDG configuration path
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decoding config: %w", err)
	}
	return cfg, nil
}

func Load(path string) (Config, error) {
	cfg, err := decode(path)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	sort.Slice(cfg.Repositories, func(i, j int) bool { return cfg.Repositories[i].ID < cfg.Repositories[j].ID })
	return cfg, nil
}

func LoadForRefresh(path string) (Config, error) { return decode(path) }

func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	sort.Slice(cfg.Repositories, func(i, j int) bool { return cfg.Repositories[i].ID < cfg.Repositories[j].ID })
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	return state.AtomicWrite(path, append(data, '\n'), 0o600)
}
