package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
)

func addAllowedRoot(repo config.Repository, root string) config.Repository {
	for _, existing := range repo.AllowedRoots {
		if existing == root {
			return repo
		}
	}
	repo.AllowedRoots = append(repo.AllowedRoots, root)
	return repo
}

func setCreationRoot(repo config.Repository, root string) config.Repository {
	repo = addAllowedRoot(repo, root)
	repo.WorktreeCreationRoot = root
	return repo
}

func removeAllowedRoot(repo config.Repository, snapshot gitclient.Snapshot, root string) (config.Repository, error) {
	if !snapshot.Complete {
		return repo, fmt.Errorf("git snapshot is incomplete; refusing allowed-root removal")
	}
	if root == repo.WorktreeCreationRoot {
		return repo, fmt.Errorf("active worktree creation root cannot be removed")
	}
	found := false
	remaining := make([]string, 0, len(repo.AllowedRoots)-1)
	for _, existing := range repo.AllowedRoots {
		if existing == root {
			found = true
			continue
		}
		remaining = append(remaining, existing)
	}
	if !found {
		return repo, fmt.Errorf("allowed worktree root %q is not configured", root)
	}
	for _, worktree := range snapshot.Worktrees {
		if worktree.Path == repo.PrimaryRoot || worktree.Prunable != "" || worktree.Exclusion != "" {
			continue
		}
		covered := false
		for _, retained := range remaining {
			if config.Contains(retained, worktree.Path) {
				covered = true
				break
			}
		}
		if !covered {
			if config.Contains(root, worktree.Path) {
				return repo, fmt.Errorf("worktree %q still depends on allowed root %q", worktree.Path, root)
			}
			return repo, fmt.Errorf("worktree %q is not covered by the retained allowed roots", worktree.Path)
		}
	}
	repo.AllowedRoots = remaining
	return repo, nil
}

func replaceRepository(cfg config.Config, updated config.Repository) config.Config {
	for i := range cfg.Repositories {
		if cfg.Repositories[i].ID == updated.ID {
			cfg.Repositories[i] = updated
			break
		}
	}
	return cfg
}

func rootsOutput(repo config.Repository) string {
	lines := []string{"creation\t" + repo.WorktreeCreationRoot}
	for _, root := range repo.AllowedRoots {
		lines = append(lines, "allowed\t"+root)
	}
	return strings.Join(lines, "\n")
}

func rootsNextStep(repo config.Repository, change string) string {
	return fmt.Sprintf("%s for %s; the running daemon will reconcile automatically; otherwise run wts reconcile --repo-id %s", change, repo.ID, repo.ID)
}

func (s *Service) repositoryRoots(ctx context.Context, action, value, repoID string) (string, error) {
	if action == "show" {
		cfg, err := config.Load(s.paths.Config)
		if err != nil {
			return "", err
		}
		repo, err := s.resolveRepo(ctx, cfg, repoID)
		if err != nil {
			return "", err
		}
		return rootsOutput(repo), nil
	}
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	repo, err := s.resolveRepo(ctx, cfg, repoID)
	if err != nil {
		return "", err
	}
	canonical, err := config.CanonicalExisting(value)
	if err != nil {
		return "", err
	}
	var change string
	switch action {
	case "set-creation":
		repo = setCreationRoot(repo, canonical)
		change = "creation root set to " + canonical
	case "add-allowed":
		repo = addAllowedRoot(repo, canonical)
		change = "allowed root added: " + canonical
	case "remove-allowed":
		_, snapshot, snapshotErr := s.snapshotRepo(ctx, cfg, repo)
		if snapshotErr != nil {
			return "", snapshotErr
		}
		repo, err = removeAllowedRoot(repo, snapshot, canonical)
		if err != nil {
			return "", err
		}
		change = "allowed root removed: " + canonical
	default:
		return "", fmt.Errorf("unknown repository roots action %q", action)
	}
	cfg = replaceRepository(cfg, repo)
	if err := config.Save(s.paths.Config, cfg); err != nil {
		return "", err
	}
	return rootsNextStep(repo, change), nil
}
