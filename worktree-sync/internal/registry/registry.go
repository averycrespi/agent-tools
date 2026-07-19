package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
)

type inspector interface {
	InspectPrimary(context.Context, string) (gitclient.Repository, error)
}

type Service struct {
	git   inspector
	paths config.Paths
}

func New(git inspector, paths config.Paths) *Service { return &Service{git: git, paths: paths} }

type AddOptions struct {
	Path                  string
	ID                    string
	CreationRoot          string
	AllowedRoots          []string
	NoDefaultAllowedRoots bool
}

func (s *Service) Add(ctx context.Context, cfg config.Config, options AddOptions) (config.Config, config.Repository, error) {
	info, err := s.git.InspectPrimary(ctx, options.Path)
	if err != nil {
		return cfg, config.Repository{}, err
	}
	for _, existing := range cfg.Repositories {
		if existing.Identity() == info.CommonGitDir {
			return cfg, config.Repository{}, fmt.Errorf("repository identity is already registered as %q", existing.ID)
		}
	}
	id := options.ID
	if id == "" {
		id = filepath.Base(info.PrimaryRoot)
	}
	if !config.ValidID(id) || len(id) > 64 {
		return cfg, config.Repository{}, fmt.Errorf("repository ID %q is unsafe; supply --id using at most 64 letters, numbers, hyphens, or underscores", id)
	}
	for _, existing := range cfg.Repositories {
		if existing.ID == id {
			if options.ID == "" {
				return cfg, config.Repository{}, fmt.Errorf("default repository ID %q collides; supply --id", id)
			}
			return cfg, config.Repository{}, fmt.Errorf("repository ID %q already exists", id)
		}
	}
	creationRoot := options.CreationRoot
	if creationRoot == "" {
		creationRoot = cfg.Global.DefaultCreationRoot
	}
	if creationRoot == "" {
		if err := os.MkdirAll(s.paths.Worktrees, 0o700); err != nil {
			return cfg, config.Repository{}, fmt.Errorf("creating default worktree root: %w", err)
		}
		if err := os.Chmod(s.paths.Worktrees, 0o700); err != nil { //nolint:gosec // private directory requires owner traversal
			return cfg, config.Repository{}, fmt.Errorf("securing default worktree root: %w", err)
		}
		creationRoot = s.paths.Worktrees
	}
	canonicalCreationRoot, err := config.CanonicalExisting(creationRoot)
	if err != nil {
		return cfg, config.Repository{}, fmt.Errorf("worktree creation root: %w", err)
	}
	requestedAllowedRoots := options.AllowedRoots
	if len(requestedAllowedRoots) == 0 && !options.NoDefaultAllowedRoots {
		requestedAllowedRoots = cfg.Global.DefaultAllowedRoots
	}
	roots := make([]string, 0, len(requestedAllowedRoots)+1)
	roots = append(roots, canonicalCreationRoot)
	seen := map[string]bool{canonicalCreationRoot: true}
	for _, root := range requestedAllowedRoots {
		canonical, canonicalErr := config.CanonicalExisting(root)
		if canonicalErr != nil {
			return cfg, config.Repository{}, fmt.Errorf("allowed worktree root: %w", canonicalErr)
		}
		if !seen[canonical] {
			roots = append(roots, canonical)
			seen[canonical] = true
		}
	}
	repo := config.Repository{ID: id, PrimaryRoot: info.PrimaryRoot, CommonGitDir: info.CommonGitDir, WorktreeCreationRoot: canonicalCreationRoot, AllowedRoots: roots, SetupPolicy: config.ActionManual, LaunchPolicy: config.ActionManual}
	cfg.Repositories = append(cfg.Repositories, repo)
	if err := cfg.Validate(); err != nil {
		return cfg, config.Repository{}, err
	}
	return cfg, repo, nil
}

func Find(cfg config.Config, id string) (config.Repository, bool) {
	for _, repo := range cfg.Repositories {
		if repo.ID == id {
			return repo, true
		}
	}
	return config.Repository{}, false
}

func Remove(cfg config.Config, id string) (config.Config, config.Repository, error) {
	for i, repo := range cfg.Repositories {
		if repo.ID == id {
			cfg.Repositories = append(cfg.Repositories[:i:i], cfg.Repositories[i+1:]...)
			return cfg, repo, nil
		}
	}
	return cfg, config.Repository{}, fmt.Errorf("repository %q is not registered", id)
}
