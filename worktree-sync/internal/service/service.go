package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
func optionInt(options map[string]any, key string) int {
	value, _ := options[key].(int)
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
	case "repo.roots.show":
		return s.repositoryRoots(ctx, "show", "", optionString(request.Options, "repo_id"))
	case "repo.roots.set-creation", "repo.roots.add-allowed", "repo.roots.remove-allowed":
		return s.repositoryRoots(ctx, strings.TrimPrefix(request.Action, "repo.roots."), request.Args[0], optionString(request.Options, "repo_id"))
	case "worktree.path":
		return s.worktreePath(ctx, request.Args[0], optionString(request.Options, "repo_id"))
	case "worktree.create":
		return s.createWorktree(ctx, request)
	case "worktree.remove":
		return s.removeWorktree(ctx, request)
	case "worktree.setup":
		return s.runSetup(ctx, request)
	case "worktree.launch":
		return s.runLaunch(ctx, request)
	case "attach":
		return "", s.attach(ctx, optionString(request.Options, "repo_id"))
	case "status":
		return s.status(ctx, optionString(request.Options, "repo_id"), optionBool(request.Options, "all"), optionBool(request.Options, "json"), optionBool(request.Options, "verbose"), optionBool(request.Options, "check"))
	case "reconcile":
		return s.reconcile(ctx, optionString(request.Options, "repo_id"), optionBool(request.Options, "all"), optionBool(request.Options, "dry_run"))
	case "cleanup":
		return s.cleanup(ctx, optionString(request.Options, "prune_git"), optionString(request.Options, "remove_orphaned_tmux"))
	case "doctor":
		return s.doctor(ctx, optionBool(request.Options, "json"))
	case "daemon.install", "daemon.uninstall", "daemon.start", "daemon.stop", "daemon.restart", "daemon.status", "daemon.logs":
		return s.daemon(ctx, strings.TrimPrefix(request.Action, "daemon."), request.Options)
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
	dir := filepath.Dir(s.paths.Config)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".config-edit-*.json")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer func() { _ = os.Remove(path) }()
	current, err := os.ReadFile(s.paths.Config) //nolint:gosec // fixed application config path
	if errors.Is(err, os.ErrNotExist) {
		if closeErr := tmp.Close(); closeErr != nil {
			return "", closeErr
		}
		if err := config.Save(path, config.Default()); err != nil {
			return "", err
		}
	} else {
		if err != nil {
			_ = tmp.Close()
			return "", err
		}
		if _, err := tmp.Write(current); err != nil {
			_ = tmp.Close()
			return "", err
		}
		if err := tmp.Close(); err != nil {
			return "", err
		}
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
	edited, err := os.ReadFile(path) //nolint:gosec // private editor temporary file
	if err != nil {
		return "", err
	}
	if err := state.AtomicWrite(s.paths.Config, edited, 0o600); err != nil {
		if state.CommitUncertain(err) {
			output := "configuration bytes were replaced but directory sync failed; durability is uncertain\nnext: run wts config validate, then wts config edit if repair is needed"
			return output, err
		}
		return "", err
	}
	if _, err := config.Load(s.paths.Config); err != nil {
		output := fmt.Sprintf("configuration updated but is invalid: %v\nnext: run wts config edit to continue repairing it", err)
		return output, fmt.Errorf("configuration is invalid: %w", err)
	}
	return "configuration updated and valid", nil
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
	updated, repo, err := registry.New(git, s.paths).Add(ctx, cfg, registry.AddOptions{Path: path, ID: optionString(request.Options, "id"), CreationRoot: optionString(request.Options, "root"), AllowedRoots: optionStrings(request.Options, "roots"), NoDefaultAllowedRoots: optionBool(request.Options, "no_default_allowed_roots")})
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
		if session.Metadata == (tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}) {
			return session
		}
	}
	return nil
}

func attachSession(snapshot tmux.Snapshot, repo config.Repository) (tmux.Session, error) {
	if !snapshot.Complete {
		return tmux.Session{}, fmt.Errorf("tmux snapshot is incomplete; refusing attach")
	}
	expectedName := "wts-" + repo.ID
	expectedMetadata := tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}
	owned := make([]tmux.Session, 0, 1)
	for _, session := range snapshot.Sessions {
		if session.Name == expectedName && session.Metadata != expectedMetadata {
			return tmux.Session{}, fmt.Errorf("foreign or untagged session %q conflicts with repository %q", expectedName, repo.ID)
		}
		if session.Metadata == expectedMetadata {
			owned = append(owned, session)
		}
	}
	if len(owned) == 0 {
		return tmux.Session{}, fmt.Errorf("managed session is not present; run wts reconcile --repo-id %s", repo.ID)
	}
	if len(owned) > 1 {
		return tmux.Session{}, fmt.Errorf("multiple owned sessions exist for repository %q; reconcile or consolidate them before attaching", repo.ID)
	}
	return owned[0], nil
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
	removed := 0
	for _, session := range snapshot.Sessions {
		for _, window := range session.Windows {
			if !tmux.ValidOwnedWindow(window.Metadata, repo.Identity()) {
				continue
			}
			owned, ownershipErr := tmuxClient.OwnsWindow(ctx, window.ID, window.Metadata)
			if ownershipErr != nil || !owned {
				output := fmt.Sprintf("removed %d owned tmux windows; repository %s remains registered\nnext: inspect ownership, then retry wts repo remove %s", removed, id, id)
				return output, fmt.Errorf("tmux ownership changed; repository remains registered")
			}
			if err := tmuxClient.KillWindow(ctx, window.ID); err != nil {
				output := fmt.Sprintf("removed %d owned tmux windows; repository %s remains registered\nnext: correct tmux availability, then retry wts repo remove %s", removed, id, id)
				return output, fmt.Errorf("tmux cleanup failed; repository remains registered: %w", err)
			}
			removed++
		}
	}
	if err := config.Save(s.paths.Config, updated); err != nil {
		output := fmt.Sprintf("removed %d owned tmux windows; repository %s remains registered because config update failed\nnext: repair the config path, then retry wts repo remove %s", removed, id, id)
		return output, fmt.Errorf("tmux resources removed but config update failed: %w", err)
	}
	return fmt.Sprintf("unregistered %s; removed %d owned tmux windows; Git worktrees and branches unchanged", id, removed), nil
}

func (s *Service) resolveRepo(ctx context.Context, cfg config.Config, id string) (config.Repository, error) {
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
	git, _, _, _, _, err := s.runtime(cfg)
	if err != nil {
		return config.Repository{}, err
	}
	current, err := git.InspectContext(ctx, cwd)
	if errors.Is(err, gitclient.ErrNotWorktree) {
		if len(cfg.Repositories) == 0 {
			return config.Repository{}, fmt.Errorf("no repositories are registered; run wts repo add from a primary Git worktree")
		}
		return config.Repository{}, fmt.Errorf("current directory is not inside a registered Git worktree; run from one or supply --repo-id <id>")
	}
	if err != nil {
		return config.Repository{}, fmt.Errorf("current Git context could not be inspected; supply --repo-id <id>: %w", err)
	}
	matches := make([]config.Repository, 0, 1)
	for _, repo := range cfg.Repositories {
		if repo.Identity() == current.CommonGitDir {
			matches = append(matches, repo)
		}
	}
	if len(matches) == 0 {
		if len(cfg.Repositories) == 0 {
			return config.Repository{}, fmt.Errorf("no repositories are registered; run wts repo add %q", current.PrimaryRoot)
		}
		return config.Repository{}, fmt.Errorf("current Git repository is not registered; run wts repo add %q", current.PrimaryRoot)
	}
	if len(matches) > 1 {
		return config.Repository{}, fmt.Errorf("current Git repository matches multiple registrations; supply --repo-id <id>")
	}
	return matches[0], nil
}

func (s *Service) pathFor(repo config.Repository, branch string) string {
	occupied := func(path string) bool { _, err := os.Lstat(path); return err == nil }
	return naming.Path(repo.WorktreeCreationRoot, repo.ID, branch, repo.Identity()+"\x00"+branch, occupied)
}

func (s *Service) worktreePath(ctx context.Context, branch, repoID string) (string, error) {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	repo, err := s.resolveRepo(ctx, cfg, repoID)
	if err != nil {
		return "", err
	}
	_, snapshot, err := s.snapshotRepo(ctx, cfg, repo)
	if err != nil {
		return "", err
	}
	for _, worktree := range snapshot.Worktrees {
		if worktree.Branch == branch && worktree.Exclusion == "" {
			return worktree.Path, nil
		}
	}
	return s.pathFor(repo, branch), nil
}

func createdWorktreePartial(path, warning, recovery string) string {
	return fmt.Sprintf("worktree created: %s\nwarning: %s\nnext: %s", path, warning, recovery)
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
	repo, err := s.resolveRepo(ctx, cfg, optionString(request.Options, "repo_id"))
	if err != nil {
		return "", err
	}
	git, _, _, executor, _, err := s.runtime(cfg)
	if err != nil {
		return "", err
	}
	branch := request.Args[0]
	path := s.pathFor(repo, branch)
	creationRoot, err := os.OpenRoot(repo.WorktreeCreationRoot)
	if err != nil {
		return "", fmt.Errorf("opening worktree creation root: %w", err)
	}
	if err := creationRoot.Mkdir(repo.ID, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		_ = creationRoot.Close()
		return "", fmt.Errorf("creating repository worktree root: %w", err)
	}
	_ = creationRoot.Close()
	parent, err := config.CanonicalExisting(filepath.Dir(path))
	if err != nil || !config.Contains(repo.WorktreeCreationRoot, parent) {
		return "", fmt.Errorf("worktree path parent is not safely contained by the creation root")
	}
	exists, err := git.BranchExists(ctx, repo.PrimaryRoot, branch)
	if err != nil {
		return "", err
	}
	if err := git.Add(ctx, repo.PrimaryRoot, path, branch, optionString(request.Options, "from"), exists); err != nil {
		return "", err
	}
	canonical, err := config.CanonicalExisting(path)
	if err != nil {
		return createdWorktreePartial(path, "created path could not be canonicalized", "inspect the created worktree before retrying"), err
	}
	snapshot, err := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir})
	if err != nil {
		return createdWorktreePartial(path, "explicit provenance could not be established", "repair Git metadata, then run wts reconcile --repo-id "+repo.ID), fmt.Errorf("worktree created but explicit provenance could not be established: %w", err)
	}
	createdWorktree, err := findWorktree(snapshot, canonical)
	if err != nil || createdWorktree.Identity == "" {
		return createdWorktreePartial(path, "Git identity is unavailable", "repair Git metadata, then run wts reconcile --repo-id "+repo.ID), fmt.Errorf("worktree created but its Git identity is unavailable")
	}
	provenancePath := filepath.Join(s.paths.State, "provenance.json")
	provenance, err := state.LoadProvenance(provenancePath)
	if err != nil {
		return createdWorktreePartial(path, "provenance state could not be read", "repair the private state path, then run wts reconcile --repo-id "+repo.ID), err
	}
	provenance.RecordExplicit(repo.Identity(), canonical, createdWorktree.Identity)
	if err := provenance.Save(provenancePath); err != nil {
		return createdWorktreePartial(path, "explicit provenance could not be saved", "repair the private state path, then run wts reconcile --repo-id "+repo.ID), err
	}
	report := executor.ReconcileRepo(ctx, repo, func(path, identity string) actions.Trigger {
		if provenance.Explicit(repo.Identity(), path, identity) {
			return actions.Explicit
		}
		return actions.Passive
	})
	if len(report.Errors) > 0 {
		return createdWorktreePartial(path, "reconciliation was degraded", "correct the reported cause, then run wts reconcile --repo-id "+repo.ID), fmt.Errorf("worktree created; reconciliation degraded: %s", strings.Join(report.Errors, "; "))
	}
	return path, nil
}

func (s *Service) snapshotRepo(ctx context.Context, cfg config.Config, repo config.Repository) (*gitclient.Client, gitclient.Snapshot, error) {
	git, _, _, _, _, err := s.runtime(cfg)
	if err != nil {
		return nil, gitclient.Snapshot{}, err
	}
	snapshot, err := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir})
	return git, snapshot, err
}
func findWorktree(snapshot gitclient.Snapshot, value string) (gitclient.Worktree, error) {
	for _, worktree := range snapshot.Worktrees {
		if worktree.Branch == value {
			return worktree, nil
		}
	}
	canonical, err := config.CanonicalExisting(value)
	if err == nil {
		for _, worktree := range snapshot.Worktrees {
			if worktree.Path == canonical {
				return worktree, nil
			}
		}
	}
	return gitclient.Worktree{}, fmt.Errorf("worktree %q is not enumerated by Git", value)
}

func removedWorktreePartial(path, warning, recovery string) string {
	return fmt.Sprintf("worktree removed: %s\nwarning: %s\nnext: %s", path, warning, recovery)
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
	repo, err := s.resolveRepo(ctx, cfg, optionString(request.Options, "repo_id"))
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
	provenance.Remove(repo.Identity(), worktree.Path, worktree.Identity)
	if err := git.Remove(ctx, repo.PrimaryRoot, worktree.Path, optionBool(request.Options, "force")); err != nil {
		return "", err
	}
	if err := provenance.Save(provenancePath); err != nil {
		return removedWorktreePartial(worktree.Path, "provenance state was not updated", "repair the private state path; the Git worktree is already removed"), fmt.Errorf("worktree removed but provenance update failed: %w", err)
	}
	if optionBool(request.Options, "delete_branch") || optionBool(request.Options, "force_delete_branch") {
		if worktree.Branch == "" {
			return removedWorktreePartial(worktree.Path, "detached worktree has no branch to delete", "no branch action is required"), fmt.Errorf("detached worktree has no branch to delete")
		}
		if err := git.DeleteBranch(ctx, repo.PrimaryRoot, worktree.Branch, optionBool(request.Options, "force_delete_branch")); err != nil {
			return removedWorktreePartial(worktree.Path, "branch "+worktree.Branch+" was not deleted", "resolve branch safety, then delete it explicitly with Git"), fmt.Errorf("worktree removed but branch deletion failed: %w", err)
		}
		return fmt.Sprintf("worktree removed: %s\nbranch deleted: %s", worktree.Path, worktree.Branch), nil
	}
	return "worktree removed: " + worktree.Path + "\nbranch preserved", nil
}

func formatSetupResult(path, repoID string, result actions.Result) (string, error) {
	if result.Skipped {
		output := fmt.Sprintf("setup skipped for %s: %s", path, result.Reason)
		if result.Code == actions.ResultSkippedAttempted {
			output += fmt.Sprintf("; run wts worktree setup %q --repo-id %s --rerun to retry", path, repoID)
		}
		return output, nil
	}
	if result.Error != nil {
		operationCompleted, attemptRecorded := "no", "no"
		if result.OperationCompleted {
			operationCompleted = "yes"
		}
		if result.AttemptRecorded {
			attemptRecorded = "yes"
		} else if result.AttemptRecordUncertain {
			attemptRecorded = "unknown"
		}
		output := fmt.Sprintf("setup failed in %s\noperation_completed=%s attempt_recorded=%s", path, operationCompleted, attemptRecorded)
		if !result.OperationCompleted {
			output += "\nwarning: earlier setup side effects may remain"
		}
		switch {
		case result.OperationCompleted && !result.AttemptRecorded:
			output += "\nnext: setup completed but ledger persistence is incomplete or uncertain; do not rerun until the state path is repaired and side effects are inspected"
		case result.AttemptRecordUncertain:
			output += "\nnext: inspect the action ledger and setup side effects before deciding whether an explicit --rerun is safe"
		case result.AttemptRecorded:
			output += fmt.Sprintf("\nnext: inspect earlier side effects, correct the cause, then run wts worktree setup %q --repo-id %s --rerun if safe", path, repoID)
		default:
			output += fmt.Sprintf("\nnext: no attempt was recorded; correct the cause, then retry wts worktree setup %q --repo-id %s", path, repoID)
		}
		return output, result.Error
	}
	return "setup completed in " + path, nil
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
	repo, err := s.resolveRepo(ctx, cfg, optionString(request.Options, "repo_id"))
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
	_, tmuxClient, manager, _, _, err := s.runtime(cfg)
	if err != nil {
		return "", err
	}
	revalidate := func() error {
		fresh, snapshotErr := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir})
		if snapshotErr != nil {
			return snapshotErr
		}
		if !fresh.Complete {
			return fmt.Errorf("worktree revalidation incomplete")
		}
		for _, candidate := range fresh.Worktrees {
			if candidate.Path == worktree.Path && candidate.Identity == worktree.Identity && candidate.Exclusion == "" {
				return nil
			}
		}
		return fmt.Errorf("worktree identity or path changed; refusing action")
	}
	actionWorktree := actions.Worktree{Path: worktree.Path, Identity: worktree.Identity}
	rerun := optionBool(request.Options, "rerun")
	if !launch {
		if err := revalidate(); err != nil {
			return "", err
		}
		result := manager.Run(ctx, repo, actionWorktree, actions.Manual, rerun)
		return formatSetupResult(worktree.Path, repo.ID, result)
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
	expected := tmux.Metadata{}
	for _, window := range session.Windows {
		if tmux.ValidOwnedWindow(window.Metadata, repo.Identity()) && window.Metadata.Role == "worktree" && window.Metadata.Identity == worktree.Identity {
			windowID, expected = window.ID, window.Metadata
			break
		}
	}
	if windowID == "" {
		return "", fmt.Errorf("managed worktree window is not present")
	}
	result := manager.Launch(ctx, repo, actionWorktree, actions.Manual, rerun, func() error {
		if err := revalidate(); err != nil {
			return err
		}
		owned, ownershipErr := tmuxClient.OwnsWindow(ctx, windowID, expected)
		if ownershipErr != nil {
			return ownershipErr
		}
		if !owned {
			return fmt.Errorf("ownership changed; refusing launch")
		}
		return tmuxClient.Launch(ctx, windowID, expected, repo.LaunchCommand)
	})
	if result.Skipped {
		return "launch skipped: " + result.Reason, nil
	}
	if result.Error != nil {
		textSent, enterSent := "no", "no"
		if result.OperationCompleted {
			textSent, enterSent = "yes", "yes"
		} else {
			var delivery *tmux.LaunchError
			if errors.As(result.Error, &delivery) {
				textSent, enterSent = delivery.TextSent, delivery.EnterSent
			}
		}
		attemptRecorded := "no"
		if result.AttemptRecorded {
			attemptRecorded = "yes"
		} else if result.AttemptRecordUncertain {
			attemptRecorded = "unknown"
		}
		output := fmt.Sprintf("launch failed in %s\ntext_sent=%s enter_sent=%s attempt_recorded=%s\nnext: inspect the managed window, then run wts worktree launch %q --repo-id %s --rerun if safe", worktree.Path, textSent, enterSent, attemptRecorded, worktree.Path, repo.ID)
		return output, result.Error
	}
	return "launch started in " + worktree.Path, nil
}
func (s *Service) runSetup(ctx context.Context, request app.Request) (string, error) {
	return s.explicitAction(ctx, request, false)
}
func (s *Service) runLaunch(ctx context.Context, request app.Request) (string, error) {
	return s.explicitAction(ctx, request, true)
}

func (s *Service) attach(ctx context.Context, repoID string) error {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return err
	}
	repo, err := s.resolveRepo(ctx, cfg, repoID)
	if err != nil {
		return err
	}
	_, tmuxClient, _, _, _, err := s.runtime(cfg)
	if err != nil {
		return err
	}
	snapshot, err := tmuxClient.Snapshot(ctx)
	if err != nil {
		return err
	}
	session, err := attachSession(snapshot, repo)
	if err != nil {
		return err
	}
	owned, err := tmuxClient.OwnsSession(ctx, session.ID, session.Metadata)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("session ownership changed; refusing attach")
	}
	return tmuxClient.Attach(ctx, session.ID)
}

func describeReconcileOperation(operation reconcile.Operation) string {
	switch operation.Type {
	case reconcile.CreateSession:
		return fmt.Sprintf("create session %q at %s", operation.Name, operation.Path)
	case reconcile.RepairSession:
		return fmt.Sprintf("repair session %s name to %q", operation.TargetID, operation.Name)
	case reconcile.CreateWindow:
		return fmt.Sprintf("create %s window %q at %s", operation.Role, operation.Name, operation.Path)
	case reconcile.RepairWindow:
		description := fmt.Sprintf("repair %s window %s to %q at %s", operation.Role, operation.TargetID, operation.Name, operation.Path)
		if operation.RespawnsPane {
			description += " (respawns pane with respawn-window -k)"
		}
		return description
	case reconcile.KillWindow:
		return fmt.Sprintf("remove %s window %s", operation.Role, operation.TargetID)
	default:
		return string(operation.Type)
	}
}

func renderReconcileReport(repoID string, report reconcile.Report, dryRun bool) string {
	lines := []string{repoID + ":"}
	applied, partial, failed := 0, 0, 0
	if dryRun {
		for _, operation := range report.Plan.Operations {
			lines = append(lines, "  planned: "+describeReconcileOperation(operation))
		}
		if len(report.Plan.Operations) == 0 && len(report.Errors) == 0 {
			lines = append(lines, "  planned: no changes")
		}
	} else {
		for _, outcome := range report.Outcomes {
			line := fmt.Sprintf("  %s: %s", outcome.Status, describeReconcileOperation(outcome.Operation))
			if len(outcome.Effects) > 0 {
				line += " [" + strings.Join(outcome.Effects, ", ") + "]"
			}
			if outcome.Error != "" {
				line += ": " + outcome.Error
			}
			lines = append(lines, line)
			switch outcome.Status {
			case reconcile.OutcomeApplied:
				applied++
			case reconcile.OutcomePartial:
				partial++
			case reconcile.OutcomeFailed:
				failed++
			}
		}
		if len(report.Outcomes) == 0 && len(report.Errors) == 0 {
			lines = append(lines, "  applied: no changes")
		}
	}
	for _, outcome := range report.ActionOutcomes {
		line := fmt.Sprintf("  %s action: %s %s operation_completed=%s attempt_recorded=%s", outcome.Status, outcome.Action, outcome.Path, outcome.OperationCompleted, outcome.AttemptRecorded)
		if outcome.TextSent != "" {
			line += fmt.Sprintf(" text_sent=%s enter_sent=%s", outcome.TextSent, outcome.EnterSent)
		}
		if outcome.Error != "" {
			line += ": " + outcome.Error
		}
		lines = append(lines, line)
		if outcome.Recovery != "" {
			lines = append(lines, "  action recovery: "+outcome.Recovery)
		}
	}
	for _, note := range report.Plan.Report {
		lines = append(lines, "  note: "+note)
	}
	for _, diagnostic := range report.Errors {
		lines = append(lines, "  error: "+diagnostic)
	}
	if dryRun {
		lines = append(lines, fmt.Sprintf("  summary: %d planned, %d conflicts, %d errors", len(report.Plan.Operations), len(report.Plan.Conflicts), len(report.Errors)))
		lines = append(lines, "  note: dry-run does not reserve or approve a future apply")
	} else {
		lines = append(lines, fmt.Sprintf("  summary: %d applied, %d partial, %d failed, %d action outcomes, %d errors", applied, partial, failed, len(report.ActionOutcomes), len(report.Errors)))
	}
	if len(report.Errors) > 0 {
		lines = append(lines, fmt.Sprintf("  next: follow any action recovery above; otherwise inspect completed effects, then run wts reconcile --repo-id %q when safe", repoID))
	}
	return strings.Join(lines, "\n")
}

func (s *Service) reconcile(ctx context.Context, repoID string, all, dryRun bool) (string, error) {
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
	if !all {
		repo, resolveErr := s.resolveRepo(ctx, cfg, repoID)
		if resolveErr != nil {
			return "", resolveErr
		}
		repositories = []config.Repository{repo}
	}
	var provenance *state.Provenance
	if !dryRun {
		provenance, err = state.LoadProvenance(filepath.Join(s.paths.State, "provenance.json"))
		if err != nil {
			return "", err
		}
	}
	_, tmuxClient, _, executor, _, err := s.runtime(cfg)
	if err != nil {
		return "", err
	}
	actual, err := tmuxClient.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	summaries := make([]string, 0, len(repositories))
	var errs []error
	for _, repo := range repositories {
		var report reconcile.Report
		if dryRun {
			report = executor.PreviewRepoWithSnapshot(ctx, repo, actual)
		} else {
			report = executor.ReconcileRepoWithSnapshot(ctx, repo, actual, func(path, identity string) actions.Trigger {
				if provenance.Explicit(repo.Identity(), path, identity) {
					return actions.Explicit
				}
				return actions.Passive
			})
		}
		summaries = append(summaries, renderReconcileReport(repo.ID, report, dryRun))
		if len(report.Errors) > 0 {
			errs = append(errs, fmt.Errorf("%s: %s", repo.ID, strings.Join(report.Errors, "; ")))
		}
	}
	return strings.Join(summaries, "\n"), errors.Join(errs...)
}

func cleanupPartial(lines []string, warning, recovery string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(append(lines, "warning: "+warning, "next: "+recovery), "\n")
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
		output, statusErr := s.status(ctx, "", true, false, false, false)
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
				return strings.Join(lines, "\n"), err
			}
			lines = append(lines, fmt.Sprintf("%s git prune applied: before=%d; verification pending", repo.ID, before))
		}
		afterSnapshot, afterErr := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir})
		if afterErr != nil {
			if before > 0 {
				lines = append(lines, "verification incomplete; next: retry wts cleanup --prune-git "+repo.ID)
			}
			return strings.Join(lines, "\n"), afterErr
		}
		after := 0
		for _, worktree := range afterSnapshot.Worktrees {
			if worktree.Prunable != "" {
				after++
			}
		}
		if before > 0 {
			lines[len(lines)-1] = fmt.Sprintf("%s git prunable: before=%d after=%d", repo.ID, before, after)
		} else {
			lines = append(lines, fmt.Sprintf("%s git prunable: before=0 after=%d", repo.ID, after))
		}
	}
	if orphanID != "" {
		repo, ok := registry.Find(cfg, orphanID)
		if !ok {
			return cleanupPartial(lines, "orphan cleanup did not run because the repository is not registered", "verify the repository ID before retrying"), fmt.Errorf("repository %q is not registered", orphanID)
		}
		_, gitSnapshot, gitSnapshotErr := s.snapshotRepo(ctx, cfg, repo)
		if gitSnapshotErr != nil {
			return cleanupPartial(lines, "orphan cleanup Git snapshot failed", "repair Git state, then retry wts cleanup --remove-orphaned-tmux "+repo.ID), gitSnapshotErr
		}
		_, tmuxClient, _, _, _, runtimeErr := s.runtime(cfg)
		if runtimeErr != nil {
			return cleanupPartial(lines, "orphan cleanup could not initialize", "correct configuration, then retry wts cleanup --remove-orphaned-tmux "+repo.ID), runtimeErr
		}
		actual, snapshotErr := tmuxClient.Snapshot(ctx)
		if snapshotErr != nil {
			return cleanupPartial(lines, "orphan cleanup tmux snapshot failed", "repair tmux availability, then retry wts cleanup --remove-orphaned-tmux "+repo.ID), snapshotErr
		}
		plan := reconcile.Build(repo, gitSnapshot, actual)
		desired := make(map[string]bool)
		for _, window := range plan.Desired.Windows {
			desired[window.Metadata.Identity] = true
		}
		removed := 0
		for _, session := range actual.Sessions {
			for _, window := range session.Windows {
				if !tmux.ValidOwnedWindow(window.Metadata, repo.Identity()) || desired[window.Metadata.Identity] {
					continue
				}
				owned, ownershipErr := tmuxClient.OwnsWindow(ctx, window.ID, window.Metadata)
				if ownershipErr != nil || !owned {
					lines = append(lines, fmt.Sprintf("%s orphaned tmux: removed=%d; verification incomplete", repo.ID, removed), "next: inspect ownership, then retry wts cleanup --remove-orphaned-tmux "+repo.ID)
					return strings.Join(lines, "\n"), fmt.Errorf("tmux ownership changed; refusing orphan removal")
				}
				if err := tmuxClient.KillWindow(ctx, window.ID); err != nil {
					lines = append(lines, fmt.Sprintf("%s orphaned tmux: removed=%d; verification incomplete", repo.ID, removed), "next: correct tmux availability, then retry wts cleanup --remove-orphaned-tmux "+repo.ID)
					return strings.Join(lines, "\n"), err
				}
				removed++
			}
		}
		lines = append(lines, fmt.Sprintf("%s orphaned tmux: removed=%d", repo.ID, removed))
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Service) daemon(ctx context.Context, action string, options map[string]any) (string, error) {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	timeout, err := time.ParseDuration(cfg.Global.CommandTimeout)
	if err != nil {
		return "", err
	}
	home, err := userHome()
	if err != nil {
		return "", err
	}
	manager := launchd.New(s.runner, runtime.GOOS, home, timeout)
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
		configHome, dataHome, stateHome := s.paths.XDGHomePaths()
		return manager.Install(ctx, canonical, launchd.Environment{ConfigHome: configHome, DataHome: dataHome, StateHome: stateHome})
	case "uninstall":
		return manager.Uninstall(ctx)
	case "start":
		return manager.Start(ctx)
	case "stop":
		return manager.Stop(ctx)
	case "restart":
		return manager.Restart(ctx)
	case "status":
		return manager.Status(ctx)
	case "logs":
		return manager.Logs(ctx, optionInt(options, "lines"), optionBool(options, "follow"))
	default:
		return "", fmt.Errorf("unknown daemon action")
	}
}
func userHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user home directory: %w", err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("user home directory must be absolute")
	}
	return filepath.Clean(home), nil
}
