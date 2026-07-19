package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

const Version = 3

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type Paths struct {
	ConfigHome string
	DataHome   string
	StateHome  string
	Config     string
	Worktrees  string
	State      string
}

type Global struct {
	ReconcileInterval   string   `json:"reconcile_interval"`
	Debounce            string   `json:"debounce"`
	CommandTimeout      string   `json:"command_timeout"`
	DefaultCreationRoot string   `json:"default_worktree_creation_root,omitempty"`
	DefaultAllowedRoots []string `json:"default_allowed_worktree_roots,omitempty"`
}

type ActionPolicy string

const (
	ActionNone       ActionPolicy = "none"
	ActionManual     ActionPolicy = "manual"
	ActionWTSCreated ActionPolicy = "wts-created"
	ActionAll        ActionPolicy = "all"
)

func (p ActionPolicy) valid() bool {
	return p == ActionNone || p == ActionManual || p == ActionWTSCreated || p == ActionAll
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
	ID                   string        `json:"id"`
	PrimaryRoot          string        `json:"primary_root"`
	CommonGitDir         string        `json:"common_git_dir"`
	WorktreeCreationRoot string        `json:"worktree_creation_root"`
	AllowedRoots         []string      `json:"allowed_worktree_roots"`
	LaunchCommand        string        `json:"launch_command,omitempty"`
	CopyActions          []CopyAction  `json:"copy_actions,omitempty"`
	SetupActions         []SetupAction `json:"setup_actions,omitempty"`
	SetupPolicy          ActionPolicy  `json:"setup_policy"`
	LaunchPolicy         ActionPolicy  `json:"launch_policy"`
}

type Config struct {
	Version      int          `json:"version"`
	Global       Global       `json:"global"`
	Repositories []Repository `json:"repositories"`
}

func Default() Config {
	return Config{Version: Version, Global: Global{ReconcileInterval: "30s", Debounce: "250ms", CommandTimeout: "20s"}, Repositories: []Repository{}}
}

func (c Config) normalized() Config {
	for i := range c.Repositories {
		if c.Repositories[i].SetupPolicy == "" {
			c.Repositories[i].SetupPolicy = ActionManual
		}
		if c.Repositories[i].LaunchPolicy == "" {
			c.Repositories[i].LaunchPolicy = ActionManual
		}
	}
	return c
}

type legacyPolicy struct {
	SetupExplicit  bool `json:"setup_explicit"`
	LaunchExplicit bool `json:"launch_explicit"`
	SetupPassive   bool `json:"setup_passive"`
	LaunchPassive  bool `json:"launch_passive"`
}
type legacyGlobal struct {
	ReconcileInterval string `json:"reconcile_interval"`
	Debounce          string `json:"debounce"`
	CommandTimeout    string `json:"command_timeout"`
}

type legacyRepository struct {
	ID                 string        `json:"id"`
	PrimaryRoot        string        `json:"primary_root"`
	CommonGitDir       string        `json:"common_git_dir"`
	RepositoryIdentity string        `json:"repository_identity"`
	AllowedRoots       []string      `json:"allowed_worktree_roots"`
	LaunchCommand      string        `json:"launch_command,omitempty"`
	CopyActions        []CopyAction  `json:"copy_actions,omitempty"`
	SetupActions       []SetupAction `json:"setup_actions,omitempty"`
	Policy             legacyPolicy  `json:"policy"`
}

type legacyConfig struct {
	Version      int                `json:"version"`
	Global       legacyGlobal       `json:"global"`
	Repositories []legacyRepository `json:"repositories"`
}

type versionTwoGlobal struct {
	ReconcileInterval   string   `json:"reconcile_interval"`
	Debounce            string   `json:"debounce"`
	CommandTimeout      string   `json:"command_timeout"`
	DefaultAllowedRoots []string `json:"default_allowed_worktree_roots,omitempty"`
}

type versionTwoRepository struct {
	ID            string        `json:"id"`
	PrimaryRoot   string        `json:"primary_root"`
	CommonGitDir  string        `json:"common_git_dir"`
	AllowedRoots  []string      `json:"allowed_worktree_roots"`
	LaunchCommand string        `json:"launch_command,omitempty"`
	CopyActions   []CopyAction  `json:"copy_actions,omitempty"`
	SetupActions  []SetupAction `json:"setup_actions,omitempty"`
	SetupPolicy   ActionPolicy  `json:"setup_policy"`
	LaunchPolicy  ActionPolicy  `json:"launch_policy"`
}

type versionTwoConfig struct {
	Version      int                    `json:"version"`
	Global       versionTwoGlobal       `json:"global"`
	Repositories []versionTwoRepository `json:"repositories"`
}

func migratePolicy(explicit, passive bool) (ActionPolicy, error) {
	switch {
	case !explicit && !passive:
		return ActionNone, nil
	case explicit && !passive:
		return ActionWTSCreated, nil
	case explicit && passive:
		return ActionAll, nil
	default:
		return "", fmt.Errorf("passive-only policy cannot migrate to cumulative modes")
	}
}

func migrateLegacy(legacy legacyConfig) (Config, error) {
	cfg := Config{
		Version: Version,
		Global: Global{
			ReconcileInterval: legacy.Global.ReconcileInterval,
			Debounce:          legacy.Global.Debounce,
			CommandTimeout:    legacy.Global.CommandTimeout,
		},
		Repositories: make([]Repository, 0, len(legacy.Repositories)),
	}
	for _, old := range legacy.Repositories {
		setup, err := migratePolicy(old.Policy.SetupExplicit, old.Policy.SetupPassive)
		if err != nil {
			return Config{}, fmt.Errorf("repository %q setup policy cannot migrate: %w", old.ID, err)
		}
		launch, err := migratePolicy(old.Policy.LaunchExplicit, old.Policy.LaunchPassive)
		if err != nil {
			return Config{}, fmt.Errorf("repository %q launch policy cannot migrate: %w", old.ID, err)
		}
		repo := Repository{
			ID:            old.ID,
			PrimaryRoot:   old.PrimaryRoot,
			CommonGitDir:  old.CommonGitDir,
			AllowedRoots:  old.AllowedRoots,
			LaunchCommand: old.LaunchCommand,
			CopyActions:   old.CopyActions,
			SetupActions:  old.SetupActions,
		}
		if len(repo.AllowedRoots) > 0 {
			repo.WorktreeCreationRoot = repo.AllowedRoots[0]
		}
		if old.RepositoryIdentity != "" && old.RepositoryIdentity != repo.CommonGitDir {
			return Config{}, fmt.Errorf("repository %q identity does not match common_git_dir", old.ID)
		}
		repo.SetupPolicy, repo.LaunchPolicy = setup, launch
		cfg.Repositories = append(cfg.Repositories, repo)
	}
	return cfg, nil
}

func migrateVersionTwo(old versionTwoConfig) Config {
	cfg := Config{
		Version: Version,
		Global: Global{
			ReconcileInterval:   old.Global.ReconcileInterval,
			Debounce:            old.Global.Debounce,
			CommandTimeout:      old.Global.CommandTimeout,
			DefaultAllowedRoots: old.Global.DefaultAllowedRoots,
		},
		Repositories: make([]Repository, 0, len(old.Repositories)),
	}
	if len(cfg.Global.DefaultAllowedRoots) > 0 {
		cfg.Global.DefaultCreationRoot = cfg.Global.DefaultAllowedRoots[0]
	}
	for _, oldRepo := range old.Repositories {
		repo := Repository{
			ID:            oldRepo.ID,
			PrimaryRoot:   oldRepo.PrimaryRoot,
			CommonGitDir:  oldRepo.CommonGitDir,
			AllowedRoots:  oldRepo.AllowedRoots,
			LaunchCommand: oldRepo.LaunchCommand,
			CopyActions:   oldRepo.CopyActions,
			SetupActions:  oldRepo.SetupActions,
			SetupPolicy:   oldRepo.SetupPolicy,
			LaunchPolicy:  oldRepo.LaunchPolicy,
		}
		if len(repo.AllowedRoots) > 0 {
			repo.WorktreeCreationRoot = repo.AllowedRoots[0]
		}
		cfg.Repositories = append(cfg.Repositories, repo)
	}
	return cfg
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

func (p Paths) XDGHomePaths() (configHome, dataHome, stateHome string) {
	configHome, dataHome, stateHome = p.ConfigHome, p.DataHome, p.StateHome
	if configHome == "" {
		configHome = filepath.Dir(filepath.Dir(p.Config))
	}
	if dataHome == "" {
		dataHome = filepath.Dir(filepath.Dir(p.Worktrees))
	}
	if stateHome == "" {
		stateHome = filepath.Dir(p.State)
	}
	return configHome, dataHome, stateHome
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
	return Paths{ConfigHome: configHome, DataHome: dataHome, StateHome: stateHome, Config: filepath.Join(configHome, "worktree-sync", "config.json"), Worktrees: filepath.Join(dataHome, "worktree-sync", "worktrees"), State: filepath.Join(stateHome, "worktree-sync")}, nil
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

func (r Repository) Identity() string { return r.CommonGitDir }

func (c Config) Validate() error {
	if c.Version == 1 {
		return fmt.Errorf("config version 1 requires migration; run wts config refresh")
	}
	if c.Version != Version {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	c = c.normalized()
	if err := parsePositive("reconcile_interval", c.Global.ReconcileInterval); err != nil {
		return err
	}
	if err := parsePositive("debounce", c.Global.Debounce); err != nil {
		return err
	}
	if err := parsePositive("command_timeout", c.Global.CommandTimeout); err != nil {
		return err
	}
	if c.Global.DefaultCreationRoot != "" {
		canonical, err := CanonicalExisting(c.Global.DefaultCreationRoot)
		if err != nil {
			return fmt.Errorf("default worktree creation root: %w", err)
		}
		if canonical != filepath.Clean(c.Global.DefaultCreationRoot) {
			return fmt.Errorf("default worktree creation root %q is not canonical", c.Global.DefaultCreationRoot)
		}
	}
	defaultRoots := make(map[string]bool)
	for _, path := range c.Global.DefaultAllowedRoots {
		canonical, err := CanonicalExisting(path)
		if err != nil {
			return fmt.Errorf("default allowed worktree root: %w", err)
		}
		if canonical != filepath.Clean(path) {
			return fmt.Errorf("default allowed worktree root %q is not canonical", path)
		}
		if defaultRoots[canonical] {
			return fmt.Errorf("duplicate default allowed worktree root %q", canonical)
		}
		defaultRoots[canonical] = true
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
		identity := repo.Identity()
		if identities[identity] {
			return fmt.Errorf("duplicate repository identity %q", identity)
		}
		identities[identity] = true
	}
	for _, repo := range c.Repositories {
		if !repo.SetupPolicy.valid() {
			return fmt.Errorf("repository %q setup_policy %q is invalid", repo.ID, repo.SetupPolicy)
		}
		if !repo.LaunchPolicy.valid() {
			return fmt.Errorf("repository %q launch_policy %q is invalid", repo.ID, repo.LaunchPolicy)
		}
		if repo.PrimaryRoot == "" || repo.CommonGitDir == "" || repo.WorktreeCreationRoot == "" || len(repo.AllowedRoots) == 0 {
			return fmt.Errorf("repository %q requires primary_root, common_git_dir, worktree_creation_root, and allowed roots", repo.ID)
		}
		for _, path := range append([]string{repo.PrimaryRoot, repo.CommonGitDir, repo.WorktreeCreationRoot}, repo.AllowedRoots...) {
			canonical, err := CanonicalExisting(path)
			if err != nil {
				return fmt.Errorf("repository %q: %w", repo.ID, err)
			}
			if canonical != filepath.Clean(path) {
				return fmt.Errorf("repository %q path %q is not canonical", repo.ID, path)
			}
		}
		creationAllowed := false
		for _, root := range repo.AllowedRoots {
			if root == repo.WorktreeCreationRoot {
				creationAllowed = true
				break
			}
		}
		if !creationAllowed {
			return fmt.Errorf("repository %q worktree_creation_root must be included in allowed_worktree_roots", repo.ID)
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

func read(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller supplies the XDG configuration path
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return data, nil
}

func decodeCurrent(data []byte) (Config, error) {
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decoding config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decoding config: trailing JSON value")
	}
	return cfg.normalized(), nil
}

func decodeLegacy(data []byte) (legacyConfig, error) {
	var cfg legacyConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return legacyConfig{}, fmt.Errorf("decoding version 1 config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return legacyConfig{}, fmt.Errorf("decoding version 1 config: trailing JSON value")
	}
	return cfg, nil
}

func decodeVersionTwo(data []byte) (versionTwoConfig, error) {
	var cfg versionTwoConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return versionTwoConfig{}, fmt.Errorf("decoding version 2 config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return versionTwoConfig{}, fmt.Errorf("decoding version 2 config: trailing JSON value")
	}
	return cfg, nil
}

func decode(path string) (Config, error) {
	data, err := read(path)
	if err != nil {
		return Config{}, err
	}
	if data == nil {
		return Default(), nil
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Config{}, fmt.Errorf("decoding config: %w", err)
	}
	if header.Version == 1 || header.Version == 2 {
		return Config{}, fmt.Errorf("config version %d requires migration; run wts config refresh", header.Version)
	}
	return decodeCurrent(data)
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

func LoadForRefresh(path string) (Config, error) {
	data, err := read(path)
	if err != nil {
		return Config{}, err
	}
	if data == nil {
		return Default(), nil
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Config{}, fmt.Errorf("decoding config: %w", err)
	}
	if header.Version == 0 || header.Version == 1 {
		legacy, decodeErr := decodeLegacy(data)
		if decodeErr != nil {
			return Config{}, decodeErr
		}
		return migrateLegacy(legacy)
	}
	if header.Version == 2 {
		old, decodeErr := decodeVersionTwo(data)
		if decodeErr != nil {
			return Config{}, decodeErr
		}
		return migrateVersionTwo(old), nil
	}
	if header.Version != Version {
		return Config{}, fmt.Errorf("unsupported config version %d", header.Version)
	}
	return decodeCurrent(data)
}

func Save(path string, cfg Config) error {
	cfg = cfg.normalized()
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
