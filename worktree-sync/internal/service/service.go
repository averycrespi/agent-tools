package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/actions"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	execclient "github.com/averycrespi/agent-tools/worktree-sync/internal/exec"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/launchd"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/naming"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/reconcile"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/registry"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

type Service struct {
	runner execclient.Runner
	paths  config.Paths
	socket string
}

func New(runner execclient.Runner, paths config.Paths) *Service {
	return NewWithSocket(runner, paths, tmux.SocketName)
}

func NewWithSocket(runner execclient.Runner, paths config.Paths, socket string) *Service {
	return &Service{runner: runner, paths: paths, socket: socket}
}
func NewFromEnv() (*Service, error) {
	paths, err := config.PathsFromEnv()
	if err != nil {
		return nil, err
	}
	return New(execclient.OSRunner{}, paths), nil
}

func optionString(options map[string]any, key string) string {
	value, _ := options[key].(string)
	return value
}
func optionBool(options map[string]any, key string) bool {
	value, _ := options[key].(bool)
	return value
}
func optionStrings(options map[string]any, key string) []string {
	value, _ := options[key].([]string)
	return value
}

func (s *Service) runtime(cfg config.Config) (*gitclient.Client, *tmux.Client, *actions.Manager, *reconcile.Executor, time.Duration, error) {
	timeout, err := time.ParseDuration(cfg.Global.CommandTimeout)
	if err != nil {
		return nil, nil, nil, nil, 0, err
	}
	git := gitclient.New(s.runner, timeout)
	tmuxClient := tmux.New(s.runner, s.socket, timeout)
	actionManager := actions.New(s.runner, filepath.Join(s.paths.State, "actions.json"), timeout)
	return git, tmuxClient, actionManager, reconcile.NewExecutor(git, tmuxClient, actionManager), timeout, nil
}

func (s *Service) Execute(ctx context.Context, request app.Request) (string, error) {
	switch request.Action {
	case "config.path":
		return s.paths.Config, nil
	case "config.validate":
		_, err := config.Load(s.paths.Config)
		if err != nil {
			return "", err
		}
		return "configuration is valid", nil
	case "config.refresh":
		return s.refreshConfig(ctx)
	case "config.edit":
		return s.editConfig(ctx)
	case "repo.add":
		return s.addRepo(ctx, request)
	case "repo.list":
		return s.listRepos()
	case "repo.remove":
		return s.removeRepo(ctx, request.Args[0])
	case "worktree.path":
		return s.worktreePath(ctx, request.Args)
	case "worktree.create":
		return s.createWorktree(ctx, request)
	case "worktree.remove":
		return s.removeWorktree(ctx, request)
	case "worktree.setup":
		return s.runSetup(ctx, request)
	case "worktree.launch":
		return s.runLaunch(ctx, request)
	case "attach":
		return "", s.attach(ctx, request.Args)
	case "status":
		return s.status(ctx, request.Args, optionBool(request.Options, "json"))
	case "reconcile":
		return s.reconcile(ctx, request.Args)
	case "cleanup":
		return s.cleanup(ctx, optionString(request.Options, "prune_git"), optionString(request.Options, "remove_orphaned_tmux"))
	case "daemon.install", "daemon.uninstall", "daemon.start", "daemon.stop", "daemon.status":
		return s.daemon(ctx, strings.TrimPrefix(request.Action, "daemon."))
	default:
		return "", fmt.Errorf("unsupported action %q", request.Action)
	}
}

func (s *Service) acquire(ctx context.Context, name string) (*state.Lock, error) {
	return state.Acquire(ctx, filepath.Join(s.paths.State, name+".lock"))
}

func (s *Service) refreshConfig(ctx context.Context) (string, error) {
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.LoadForRefresh(s.paths.Config)
	if err != nil {
		return "", err
	}
	defaults := config.Default()
	if cfg.Global.ReconcileInterval == "" {
		cfg.Global.ReconcileInterval = defaults.Global.ReconcileInterval
	}
	if cfg.Global.Debounce == "" {
		cfg.Global.Debounce = defaults.Global.Debounce
	}
	if cfg.Global.CommandTimeout == "" {
		cfg.Global.CommandTimeout = defaults.Global.CommandTimeout
	}
	if cfg.Version == 0 {
		cfg.Version = defaults.Version
	}
	if err := config.Save(s.paths.Config, cfg); err != nil {
		return "", err
	}
	return "configuration refreshed", nil
}

func (s *Service) editConfig(ctx context.Context) (string, error) {
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(s.paths.Config)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".config-edit-*.json")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(path) }()
	if err := config.Save(path, cfg); err != nil {
		return "", err
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return "", fmt.Errorf("VISUAL or EDITOR must name an editor executable")
	}
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return "", fmt.Errorf("editor is empty")
	}
	editCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := s.runner.Interactive(editCtx, "", fields[0], append(fields[1:], path)...); err != nil {
		return "", fmt.Errorf("editing config: %w", err)
	}
	edited, err := config.Load(path)
	if err != nil {
		return "", fmt.Errorf("edited config is invalid: %w", err)
	}
	if err := config.Save(s.paths.Config, edited); err != nil {
		return "", err
	}
	return "configuration updated", nil
}

func (s *Service) addRepo(ctx context.Context, request app.Request) (string, error) {
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	git, _, _, _, _, err := s.runtime(cfg)
	if err != nil {
		return "", err
	}
	path := "."
	if len(request.Args) == 1 {
		path = request.Args[0]
	}
	updated, repo, err := registry.New(git, s.paths).Add(ctx, cfg, registry.AddOptions{Path: path, ID: optionString(request.Options, "id"), AllowedRoots: optionStrings(request.Options, "roots")})
	if err != nil {
		return "", err
	}
	if err := config.Save(s.paths.Config, updated); err != nil {
		return "", err
	}
	return fmt.Sprintf("registered %s (%s)", repo.ID, repo.PrimaryRoot), nil
}

func (s *Service) listRepos() (string, error) {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	if len(cfg.Repositories) == 0 {
		return "no repositories registered", nil
	}
	lines := make([]string, len(cfg.Repositories))
	for i, repo := range cfg.Repositories {
		lines[i] = fmt.Sprintf("%s\t%s", repo.ID, repo.PrimaryRoot)
	}
	return strings.Join(lines, "\n"), nil
}

func matchingSession(snapshot tmux.Snapshot, repo config.Repository) *tmux.Session {
	for i := range snapshot.Sessions {
		session := &snapshot.Sessions[i]
		if session.Metadata == (tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.RepositoryIdentity, Role: "session", Identity: repo.RepositoryIdentity}) {
			return session
		}
	}
	return nil
}

func (s *Service) removeRepo(ctx context.Context, id string) (string, error) {
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	updated, repo, err := registry.Remove(cfg, id)
	if err != nil {
		return "", err
	}
	_, tmuxClient, _, _, _, err := s.runtime(cfg)
	if err != nil {
		return "", err
	}
	snapshot, err := tmuxClient.Snapshot(ctx)
	if err != nil {
		return "", fmt.Errorf("tmux cleanup failed; repository remains registered: %w", err)
	}
	if session := matchingSession(snapshot, repo); session != nil {
		managed, unmanaged := make([]tmux.Window, 0), make([]tmux.Window, 0)
		for _, window := range session.Windows {
			if window.Metadata.Schema == tmux.MetadataSchema && window.Metadata.Repository == repo.RepositoryIdentity && (window.Metadata.Role == "base" || window.Metadata.Role == "worktree") {
				managed = append(managed, window)
			} else {
				unmanaged = append(unmanaged, window)
			}
		}
		if len(unmanaged) == 0 {
			if err := tmuxClient.KillSession(ctx, session.ID); err != nil {
				return "", fmt.Errorf("tmux cleanup failed; repository remains registered: %w", err)
			}
		} else {
			for _, window := range managed {
				if err := tmuxClient.KillWindow(ctx, window.ID); err != nil {
					return "", fmt.Errorf("tmux cleanup failed; repository remains registered: %w", err)
				}
			}
		}
	}
	if err := config.Save(s.paths.Config, updated); err != nil {
		return "", fmt.Errorf("tmux resources removed but config update failed: %w", err)
	}
	return fmt.Sprintf("unregistered %s; Git worktrees and branches unchanged", id), nil
}

func (s *Service) resolveRepo(cfg config.Config, id string) (config.Repository, error) {
	if id != "" {
		repo, ok := registry.Find(cfg, id)
		if !ok {
			return config.Repository{}, fmt.Errorf("repository %q is not registered", id)
		}
		return repo, nil
	}
	cwd, err := config.CanonicalExisting(".")
	if err != nil {
		return config.Repository{}, err
	}
	matches := make([]config.Repository, 0)
	for _, repo := range cfg.Repositories {
		if config.Contains(repo.PrimaryRoot, cwd) {
			matches = append(matches, repo)
			continue
		}
		for _, root := range repo.AllowedRoots {
			if config.Contains(root, cwd) {
				matches = append(matches, repo)
				break
			}
		}
	}
	if len(matches) == 0 {
		return config.Repository{}, fmt.Errorf("current path does not identify a registered repository; supply a repository ID")
	}
	if len(matches) > 1 {
		return config.Repository{}, fmt.Errorf("current path matches multiple repositories; supply a repository ID")
	}
	return matches[0], nil
}

func (s *Service) pathFor(repo config.Repository, branch string) string {
	occupied := func(path string) bool { _, err := os.Lstat(path); return err == nil }
	return naming.Path(repo.AllowedRoots[0], repo.ID, branch, repo.RepositoryIdentity+"\x00"+branch, occupied)
}

func (s *Service) worktreePath(ctx context.Context, args []string) (string, error) {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	id := ""
	if len(args) == 2 {
		id = args[1]
	}
	repo, err := s.resolveRepo(cfg, id)
	if err != nil {
		return "", err
	}
	_, snapshot, err := s.snapshotRepo(ctx, cfg, repo)
	if err != nil {
		return "", err
	}
	for _, worktree := range snapshot.Worktrees {
		if worktree.Branch == args[0] && worktree.Exclusion == "" {
			return worktree.Path, nil
		}
	}
	return s.pathFor(repo, args[0]), nil
}

func (s *Service) createWorktree(ctx context.Context, request app.Request) (string, error) {
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	id := ""
	if len(request.Args) == 2 {
		id = request.Args[1]
	}
	repo, err := s.resolveRepo(cfg, id)
	if err != nil {
		return "", err
	}
	git, _, _, executor, _, err := s.runtime(cfg)
	if err != nil {
		return "", err
	}
	branch := request.Args[0]
	path := s.pathFor(repo, branch)
	allowedRoot, err := os.OpenRoot(repo.AllowedRoots[0])
	if err != nil {
		return "", fmt.Errorf("opening allowed worktree root: %w", err)
	}
	if err := allowedRoot.Mkdir(repo.ID, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		_ = allowedRoot.Close()
		return "", fmt.Errorf("creating repository worktree root: %w", err)
	}
	_ = allowedRoot.Close()
	parent, err := config.CanonicalExisting(filepath.Dir(path))
	if err != nil || !config.Contains(repo.AllowedRoots[0], parent) {
		return "", fmt.Errorf("worktree path parent is not safely contained by the allowed root")
	}
	exists, err := git.BranchExists(ctx, repo.PrimaryRoot, branch)
	if err != nil {
		return "", err
	}
	if err := git.Add(ctx, repo.PrimaryRoot, path, branch, optionString(request.Options, "start"), exists); err != nil {
		return "", err
	}
	canonical, err := config.CanonicalExisting(path)
	if err != nil {
		return "", err
	}
	snapshot, err := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir, Identity: repo.RepositoryIdentity})
	if err != nil {
		return path, fmt.Errorf("worktree created but explicit provenance could not be established: %w", err)
	}
	createdWorktree, err := findWorktree(snapshot, canonical)
	if err != nil || createdWorktree.Identity == "" {
		return path, fmt.Errorf("worktree created but its Git identity is unavailable")
	}
	provenancePath := filepath.Join(s.paths.State, "provenance.json")
	provenance, err := state.LoadProvenance(provenancePath)
	if err != nil {
		return "", err
	}
	provenance.RecordExplicit(repo.RepositoryIdentity, canonical, createdWorktree.Identity)
	if err := provenance.Save(provenancePath); err != nil {
		return "", err
	}
	report := executor.ReconcileRepo(ctx, repo, func(path, identity string) actions.Trigger {
		if provenance.Explicit(repo.RepositoryIdentity, path, identity) {
			return actions.Explicit
		}
		return actions.Passive
	})
	if len(report.Errors) > 0 {
		return path, fmt.Errorf("worktree created; reconciliation degraded: %s", strings.Join(report.Errors, "; "))
	}
	return path, nil
}

func (s *Service) snapshotRepo(ctx context.Context, cfg config.Config, repo config.Repository) (*gitclient.Client, gitclient.Snapshot, error) {
	git, _, _, _, _, err := s.runtime(cfg)
	if err != nil {
		return nil, gitclient.Snapshot{}, err
	}
	snapshot, err := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir, Identity: repo.RepositoryIdentity})
	return git, snapshot, err
}
func findWorktree(snapshot gitclient.Snapshot, value string) (gitclient.Worktree, error) {
	clean := filepath.Clean(value)
	for _, worktree := range snapshot.Worktrees {
		if worktree.Path == clean || worktree.Branch == value {
			return worktree, nil
		}
	}
	return gitclient.Worktree{}, fmt.Errorf("worktree %q is not enumerated by Git", value)
}

func (s *Service) removeWorktree(ctx context.Context, request app.Request) (string, error) {
	if optionBool(request.Options, "delete_branch") && optionBool(request.Options, "force_delete_branch") {
		return "", fmt.Errorf("choose only one branch deletion flag")
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
	id := ""
	if len(request.Args) == 2 {
		id = request.Args[1]
	}
	repo, err := s.resolveRepo(cfg, id)
	if err != nil {
		return "", err
	}
	git, snapshot, err := s.snapshotRepo(ctx, cfg, repo)
	if err != nil {
		return "", err
	}
	worktree, err := findWorktree(snapshot, request.Args[0])
	if err != nil {
		return "", err
	}
	if worktree.Path == repo.PrimaryRoot {
		return "", fmt.Errorf("primary worktree cannot be removed")
	}
	provenancePath := filepath.Join(s.paths.State, "provenance.json")
	provenance, err := state.LoadProvenance(provenancePath)
	if err != nil {
		return "", err
	}
	provenance.Remove(repo.RepositoryIdentity, worktree.Path, worktree.Identity)
	if err := git.Remove(ctx, repo.PrimaryRoot, worktree.Path, optionBool(request.Options, "force")); err != nil {
		return "", err
	}
	if err := provenance.Save(provenancePath); err != nil {
		return "worktree removed", fmt.Errorf("worktree removed but provenance update failed: %w", err)
	}
	if optionBool(request.Options, "delete_branch") || optionBool(request.Options, "force_delete_branch") {
		if worktree.Branch == "" {
			return "worktree removed", fmt.Errorf("detached worktree has no branch to delete")
		}
		if err := git.DeleteBranch(ctx, repo.PrimaryRoot, worktree.Branch, optionBool(request.Options, "force_delete_branch")); err != nil {
			return "worktree removed", fmt.Errorf("worktree removed but branch deletion failed: %w", err)
		}
	}
	return "worktree removed", nil
}

func (s *Service) explicitAction(ctx context.Context, request app.Request, launch bool) (string, error) {
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	id := ""
	if len(request.Args) == 2 {
		id = request.Args[1]
	}
	repo, err := s.resolveRepo(cfg, id)
	if err != nil {
		return "", err
	}
	_, snapshot, err := s.snapshotRepo(ctx, cfg, repo)
	if err != nil {
		return "", err
	}
	worktree, err := findWorktree(snapshot, request.Args[0])
	if err != nil {
		return "", err
	}
	_, tmuxClient, manager, _, _, err := s.runtime(cfg)
	if err != nil {
		return "", err
	}
	actionWorktree := actions.Worktree{Path: worktree.Path, Identity: worktree.Identity}
	rerun := optionBool(request.Options, "rerun")
	if !launch {
		result := manager.Run(ctx, repo, actionWorktree, actions.Explicit, rerun)
		return "setup evaluated", result.Error
	}
	actual, err := tmuxClient.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	session := matchingSession(actual, repo)
	if session == nil {
		return "", fmt.Errorf("managed session is not present")
	}
	windowID := ""
	for _, window := range session.Windows {
		if window.Metadata.Identity == worktree.Identity && window.Metadata.Repository == repo.RepositoryIdentity {
			windowID = window.ID
			break
		}
	}
	if windowID == "" {
		return "", fmt.Errorf("managed worktree window is not present")
	}
	result := manager.Launch(ctx, repo, actionWorktree, actions.Explicit, rerun, func() error { return tmuxClient.Launch(ctx, windowID, repo.LaunchCommand) })
	return "launch evaluated", result.Error
}
func (s *Service) runSetup(ctx context.Context, request app.Request) (string, error) {
	return s.explicitAction(ctx, request, false)
}
func (s *Service) runLaunch(ctx context.Context, request app.Request) (string, error) {
	return s.explicitAction(ctx, request, true)
}

func (s *Service) attach(ctx context.Context, args []string) error {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return err
	}
	id := ""
	if len(args) == 1 {
		id = args[0]
	}
	repo, err := s.resolveRepo(cfg, id)
	if err != nil {
		return err
	}
	_, tmuxClient, _, _, _, err := s.runtime(cfg)
	if err != nil {
		return err
	}
	return tmuxClient.Attach(ctx, "wts-"+repo.ID)
}

func (s *Service) reconcile(ctx context.Context, args []string) (string, error) {
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	repositories := cfg.Repositories
	if len(args) == 1 {
		repo, resolveErr := s.resolveRepo(cfg, args[0])
		if resolveErr != nil {
			return "", resolveErr
		}
		repositories = []config.Repository{repo}
	}
	provenance, err := state.LoadProvenance(filepath.Join(s.paths.State, "provenance.json"))
	if err != nil {
		return "", err
	}
	_, _, _, executor, _, err := s.runtime(cfg)
	if err != nil {
		return "", err
	}
	summaries := make([]string, 0, len(repositories))
	var errs []error
	for _, repo := range repositories {
		report := executor.ReconcileRepo(ctx, repo, func(path, identity string) actions.Trigger {
			if provenance.Explicit(repo.RepositoryIdentity, path, identity) {
				return actions.Explicit
			}
			return actions.Passive
		})
		summary := fmt.Sprintf("%s: %d operations, %d conflicts, %d errors", repo.ID, len(report.Plan.Operations), len(report.Plan.Conflicts), len(report.Errors))
		if len(report.Plan.Report) > 0 {
			summary += "\n  " + strings.Join(report.Plan.Report, "\n  ")
		}
		summaries = append(summaries, summary)
		if len(report.Errors) > 0 {
			errs = append(errs, fmt.Errorf("%s: %s", repo.ID, strings.Join(report.Errors, "; ")))
		}
	}
	return strings.Join(summaries, "\n"), errors.Join(errs...)
}

func (s *Service) status(ctx context.Context, args []string, jsonOutput bool) (string, error) {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	repos := cfg.Repositories
	if len(args) == 1 {
		repo, resolveErr := s.resolveRepo(cfg, args[0])
		if resolveErr != nil {
			return "", resolveErr
		}
		repos = []config.Repository{repo}
	}
	type actionFailure struct {
		Key    string             `json:"key"`
		Result state.ActionResult `json:"result"`
	}
	type repoStatus struct {
		ID             string               `json:"id"`
		Health         string               `json:"health"`
		Desired        []gitclient.Worktree `json:"desired_worktrees"`
		Actual         []tmux.Window        `json:"actual_managed_windows"`
		Conflicts      []string             `json:"conflicts"`
		Reported       []string             `json:"reported_worktrees"`
		Prunable       []gitclient.Worktree `json:"prunable"`
		ActionFailures []actionFailure      `json:"action_failures"`
	}
	type statusDocument struct {
		Version      int          `json:"version"`
		Repositories []repoStatus `json:"repositories"`
		Daemon       string       `json:"daemon"`
	}
	document := statusDocument{Version: 1, Repositories: make([]repoStatus, 0, len(repos)), Daemon: "unsupported"}
	if runtime.GOOS == "darwin" {
		if daemonStatus, daemonErr := s.daemon(ctx, "status"); daemonErr == nil {
			document.Daemon = strings.TrimSpace(daemonStatus)
		} else {
			document.Daemon = "unavailable"
		}
	}
	ledger, _ := state.LoadLedger(filepath.Join(s.paths.State, "actions.json"))
	for _, repo := range repos {
		git, tmuxClient, _, _, _, runtimeErr := s.runtime(cfg)
		if runtimeErr != nil {
			return "", runtimeErr
		}
		gitSnapshot, gitErr := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir, Identity: repo.RepositoryIdentity})
		tmuxSnapshot, tmuxErr := tmuxClient.Snapshot(ctx)
		health := "healthy"
		if gitErr != nil || tmuxErr != nil || !gitSnapshot.Complete || !tmuxSnapshot.Complete {
			health = "degraded"
		}
		plan := reconcile.Build(repo, gitSnapshot, tmuxSnapshot)
		if len(plan.Conflicts) > 0 {
			health = "conflict"
		}
		entry := repoStatus{ID: repo.ID, Health: health, Desired: planDesiredWorktrees(gitSnapshot, plan), Conflicts: plan.Conflicts, Reported: plan.Report}
		if session := matchingSession(tmuxSnapshot, repo); session != nil {
			for _, window := range session.Windows {
				if window.Metadata.Schema == tmux.MetadataSchema && window.Metadata.Repository == repo.RepositoryIdentity {
					entry.Actual = append(entry.Actual, window)
				}
			}
		}
		for _, worktree := range gitSnapshot.Worktrees {
			if worktree.Prunable != "" {
				entry.Prunable = append(entry.Prunable, worktree)
			}
		}
		if ledger != nil {
			for encodedKey, result := range ledger.Attempts {
				key, keyErr := state.DecodeActionKey(encodedKey)
				if keyErr == nil && key.Repository == repo.RepositoryIdentity && !result.Success {
					entry.ActionFailures = append(entry.ActionFailures, actionFailure{Key: encodedKey, Result: result})
				}
			}
		}
		sort.Slice(entry.Actual, func(i, j int) bool { return entry.Actual[i].ID < entry.Actual[j].ID })
		sort.Slice(entry.ActionFailures, func(i, j int) bool { return entry.ActionFailures[i].Key < entry.ActionFailures[j].Key })
		document.Repositories = append(document.Repositories, entry)
	}
	if jsonOutput {
		data, err := json.MarshalIndent(document, "", "  ")
		return string(data), err
	}
	lines := make([]string, 0, len(document.Repositories))
	for _, repo := range document.Repositories {
		lines = append(lines, fmt.Sprintf("%s\t%s\tdesired=%d actual=%d conflicts=%d reported=%d", repo.ID, repo.Health, len(repo.Desired), len(repo.Actual), len(repo.Conflicts), len(repo.Reported)))
	}
	if len(lines) == 0 {
		return "no repositories registered", nil
	}
	return strings.Join(lines, "\n"), nil
}
func planDesiredWorktrees(snapshot gitclient.Snapshot, plan reconcile.Plan) []gitclient.Worktree {
	ids := make(map[string]bool)
	for _, window := range plan.Desired.Windows {
		ids[window.Metadata.Identity] = true
	}
	result := make([]gitclient.Worktree, 0)
	for _, worktree := range snapshot.Worktrees {
		if ids[worktree.Identity] {
			result = append(result, worktree)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Identity < result[j].Identity })
	return result
}

func (s *Service) cleanup(ctx context.Context, pruneID, orphanID string) (string, error) {
	lock, err := s.acquire(ctx, "operation")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	if pruneID == "" && orphanID == "" {
		output, statusErr := s.status(ctx, nil, false)
		return "dry run; no changes applied\n" + output, statusErr
	}
	lines := make([]string, 0)
	if pruneID != "" {
		repo, ok := registry.Find(cfg, pruneID)
		if !ok {
			return "", fmt.Errorf("repository %q is not registered", pruneID)
		}
		git, snapshot, snapshotErr := s.snapshotRepo(ctx, cfg, repo)
		if snapshotErr != nil {
			return "", snapshotErr
		}
		before := 0
		for _, worktree := range snapshot.Worktrees {
			if worktree.Prunable != "" {
				before++
			}
		}
		if before > 0 {
			if err := git.Prune(ctx, repo.PrimaryRoot); err != nil {
				return "", err
			}
		}
		afterSnapshot, afterErr := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir, Identity: repo.RepositoryIdentity})
		if afterErr != nil {
			return "", afterErr
		}
		after := 0
		for _, worktree := range afterSnapshot.Worktrees {
			if worktree.Prunable != "" {
				after++
			}
		}
		lines = append(lines, fmt.Sprintf("%s git prunable: before=%d after=%d", repo.ID, before, after))
	}
	if orphanID != "" {
		repo, ok := registry.Find(cfg, orphanID)
		if !ok {
			return "", fmt.Errorf("repository %q is not registered", orphanID)
		}
		_, gitSnapshot, gitSnapshotErr := s.snapshotRepo(ctx, cfg, repo)
		if gitSnapshotErr != nil {
			return "", gitSnapshotErr
		}
		_, tmuxClient, _, _, _, runtimeErr := s.runtime(cfg)
		if runtimeErr != nil {
			return "", runtimeErr
		}
		actual, snapshotErr := tmuxClient.Snapshot(ctx)
		if snapshotErr != nil {
			return "", snapshotErr
		}
		plan := reconcile.Build(repo, gitSnapshot, actual)
		desired := make(map[string]bool)
		for _, window := range plan.Desired.Windows {
			desired[window.Metadata.Identity] = true
		}
		removed := 0
		if session := matchingSession(actual, repo); session != nil {
			for _, window := range session.Windows {
				owned := window.Metadata.Schema == tmux.MetadataSchema && window.Metadata.Repository == repo.RepositoryIdentity && (window.Metadata.Role == "base" || window.Metadata.Role == "worktree")
				if owned && !desired[window.Metadata.Identity] {
					if err := tmuxClient.KillWindow(ctx, window.ID); err != nil {
						return "", err
					}
					removed++
				}
			}
		}
		lines = append(lines, fmt.Sprintf("%s orphaned tmux: before=%d after=0", repo.ID, removed))
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Service) daemon(ctx context.Context, action string) (string, error) {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	timeout, err := time.ParseDuration(cfg.Global.CommandTimeout)
	if err != nil {
		return "", err
	}
	manager := launchd.New(s.runner, runtime.GOOS, userHome(), timeout)
	switch action {
	case "install":
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		binary := filepath.Join(filepath.Dir(executable), "wtsd")
		canonical, err := filepath.Abs(binary)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(canonical); err != nil {
			return "", fmt.Errorf("finding sibling wtsd: %w", err)
		}
		return "LaunchAgent installed", manager.Install(ctx, canonical)
	case "uninstall":
		return "LaunchAgent uninstalled", manager.Uninstall(ctx)
	case "start":
		return "LaunchAgent started", manager.Start(ctx)
	case "stop":
		return "LaunchAgent stopped", manager.Stop(ctx)
	case "status":
		return manager.Status(ctx)
	default:
		return "", fmt.Errorf("unknown daemon action")
	}
}
func userHome() string { home, _ := os.UserHomeDir(); return home }
