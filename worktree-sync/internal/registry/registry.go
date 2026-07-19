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
	Path         string
	ID           string
	AllowedRoots []string
}

func (s *Service) Add(ctx context.Context, cfg config.Config, options AddOptions) (config.Config, config.Repository, error) {
	info, err := s.git.InspectPrimary(ctx, options.Path)
	if err != nil {
		return cfg, config.Repository{}, err
	}
	for _, existing := range cfg.Repositories {
		if existing.RepositoryIdentity == info.Identity || existing.CommonGitDir == info.CommonGitDir {
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
	roots := make([]string, 0, len(options.AllowedRoots)+1)
	if len(options.AllowedRoots) == 0 {
		if err := os.MkdirAll(s.paths.Worktrees, 0o700); err != nil {
			return cfg, config.Repository{}, fmt.Errorf("creating default worktree root: %w", err)
		}
		if err := os.Chmod(s.paths.Worktrees, 0o700); err != nil { //nolint:gosec // private directory requires owner traversal
			return cfg, config.Repository{}, fmt.Errorf("securing default worktree root: %w", err)
		}
		canonical, canonicalErr := config.CanonicalExisting(s.paths.Worktrees)
		if canonicalErr != nil {
			return cfg, config.Repository{}, canonicalErr
		}
		roots = append(roots, canonical)
	} else {
		for _, root := range options.AllowedRoots {
			canonical, canonicalErr := config.CanonicalExisting(root)
			if canonicalErr != nil {
				return cfg, config.Repository{}, fmt.Errorf("allowed worktree root: %w", canonicalErr)
			}
			roots = append(roots, canonical)
		}
	}
	repo := config.Repository{ID: id, PrimaryRoot: info.PrimaryRoot, CommonGitDir: info.CommonGitDir, RepositoryIdentity: info.Identity, AllowedRoots: roots, SetupPolicy: config.ActionManual, LaunchPolicy: config.ActionManual}
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
