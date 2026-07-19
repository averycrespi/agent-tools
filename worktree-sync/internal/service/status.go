package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/actions"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/reconcile"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

type statusHealth string
type statusSeverity string

const (
	healthHealthy   statusHealth = "healthy"
	healthAttention statusHealth = "attention"
	healthDegraded  statusHealth = "degraded"
	healthConflict  statusHealth = "conflict"

	severityWarning statusSeverity = "warning"
	severityError   statusSeverity = "error"

	codeGitSnapshotFailed       = "git_snapshot_failed"
	codeGitSnapshotIncomplete   = "git_snapshot_incomplete"
	codeTmuxSnapshotFailed      = "tmux_snapshot_failed"
	codeTmuxSnapshotIncomplete  = "tmux_snapshot_incomplete"
	codeActionLedgerUnavailable = "action_ledger_unavailable"
	codeSessionNameConflict     = "session_name_conflict"
	codeMultipleOwnedSessions   = "multiple_owned_sessions"
	codeWorktreeMissing         = "worktree_missing"
	codeWorktreeIdentity        = "worktree_identity_unavailable"
	codeWorktreeOutsideRoots    = "worktree_outside_allowed_roots"
	codeWorktreePrunable        = "worktree_prunable"
	codeSetupFailed             = "setup_failed"
	codeLaunchFailed            = "launch_failed"
	codeDaemonStopped           = "daemon_stopped"
	codeDaemonUnavailable       = "daemon_unavailable"

	reasonMissing       = "missing"
	reasonGitIdentity   = "git_identity_unavailable"
	reasonOutsideRoots  = "outside_allowed_roots"
	reasonGitPrunable   = "git_metadata_prunable"
	statusDaemonRunning = "running"
	statusDaemonStopped = "stopped"
	statusDaemonAbsent  = "not_installed"
	statusDaemonUnsup   = "unsupported"
	statusDaemonUnavail = "unavailable"
)

type statusDiagnostic struct {
	Severity statusSeverity `json:"severity"`
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Recovery string         `json:"recovery,omitempty"`
}

type statusWorktree struct {
	Path     string `json:"path"`
	HEAD     string `json:"head"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Locked   string `json:"locked,omitempty"`
	Identity string `json:"identity"`
	Eligible bool   `json:"eligible"`
}

type statusWindow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Role     string `json:"role"`
	Identity string `json:"identity"`
}

type statusReportedWorktree struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type statusActionFailure struct {
	WorktreeIdentity string `json:"worktree_identity"`
	WorktreePath     string `json:"worktree_path,omitempty"`
	Action           string `json:"action"`
	Attempted        string `json:"attempted"`
	ErrorCode        string `json:"error_code"`
	Message          string `json:"message"`
}

type statusRepository struct {
	ID                   string                   `json:"id"`
	Health               statusHealth             `json:"health"`
	Diagnostics          []statusDiagnostic       `json:"diagnostics"`
	DesiredWorktrees     []statusWorktree         `json:"desired_worktrees"`
	ActualManagedWindows []statusWindow           `json:"actual_managed_windows"`
	Conflicts            []string                 `json:"conflicts"`
	ReportedWorktrees    []statusReportedWorktree `json:"reported_worktrees"`
	PrunableWorktrees    []statusReportedWorktree `json:"prunable_worktrees"`
	ActionFailures       []statusActionFailure    `json:"action_failures"`
}

type statusDaemon struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type statusDocument struct {
	Version      int                `json:"version"`
	Repositories []statusRepository `json:"repositories"`
	Daemon       statusDaemon       `json:"daemon"`
	Diagnostics  []statusDiagnostic `json:"diagnostics"`
}

func newStatusDocument() statusDocument {
	return statusDocument{Version: 2, Repositories: []statusRepository{}, Daemon: statusDaemon{State: statusDaemonUnsup}, Diagnostics: []statusDiagnostic{}}
}

func statusRequiresAttention(document statusDocument) bool {
	if len(document.Diagnostics) > 0 {
		return true
	}
	for _, repo := range document.Repositories {
		if repo.Health != healthHealthy {
			return true
		}
	}
	return false
}

func renderStatusHuman(document statusDocument, verbose bool) string {
	if len(document.Repositories) == 0 && len(document.Diagnostics) == 0 {
		return "no repositories registered"
	}
	lines := make([]string, 0, len(document.Repositories))
	for _, repo := range document.Repositories {
		lines = append(lines, fmt.Sprintf("%s\t%s\tdesired=%d actual=%d conflicts=%d reported=%d", repo.ID, repo.Health, len(repo.DesiredWorktrees), len(repo.ActualManagedWindows), len(repo.Conflicts), len(repo.ReportedWorktrees)))
		if repo.Health != healthHealthy || verbose {
			for _, diagnostic := range repo.Diagnostics {
				lines = append(lines, fmt.Sprintf("  %s[%s]: %s", diagnostic.Severity, diagnostic.Code, diagnostic.Message))
				if diagnostic.Recovery != "" {
					lines = append(lines, "    next: "+diagnostic.Recovery)
				}
			}
			for _, conflict := range repo.Conflicts {
				lines = append(lines, "  conflict: "+conflict)
			}
			for _, worktree := range repo.ReportedWorktrees {
				lines = append(lines, fmt.Sprintf("  reported: %s — %s", worktree.Path, worktree.Reason))
			}
			for _, worktree := range repo.PrunableWorktrees {
				lines = append(lines, fmt.Sprintf("  prunable: %s — %s", worktree.Path, worktree.Reason))
			}
			for _, failure := range repo.ActionFailures {
				lines = append(lines, fmt.Sprintf("  action failure: %s %s — %s", failure.Action, failure.WorktreeIdentity, failure.Message))
			}
		}
		if verbose {
			for _, worktree := range repo.DesiredWorktrees {
				lines = append(lines, "  desired: "+worktree.Path)
			}
			for _, window := range repo.ActualManagedWindows {
				lines = append(lines, fmt.Sprintf("  actual: %s %s — %s", window.ID, window.Name, window.Path))
			}
		}
	}
	if verbose || document.Daemon.State == statusDaemonStopped || document.Daemon.State == statusDaemonUnavail {
		line := "daemon\t" + document.Daemon.State
		if document.Daemon.Message != "" {
			line += "\t" + document.Daemon.Message
		}
		lines = append(lines, line)
	}
	for _, diagnostic := range document.Diagnostics {
		lines = append(lines, fmt.Sprintf("%s[%s]: %s", diagnostic.Severity, diagnostic.Code, diagnostic.Message))
		if diagnostic.Recovery != "" {
			lines = append(lines, "  next: "+diagnostic.Recovery)
		}
	}
	return strings.Join(lines, "\n")
}

type decodedActionAttempt struct {
	key    state.ActionKey
	result state.ActionResult
}

func statusDiagnosticForReported(reported reconcile.ReportedWorktree) statusDiagnostic {
	switch reported.Reason {
	case reasonMissing:
		return statusDiagnostic{Severity: severityWarning, Code: codeWorktreeMissing, Message: reported.Path + " is missing", Recovery: "repair or prune the Git worktree record"}
	case reasonGitIdentity:
		return statusDiagnostic{Severity: severityWarning, Code: codeWorktreeIdentity, Message: reported.Path + " has no usable Git identity", Recovery: "repair the linked worktree metadata"}
	case reasonOutsideRoots:
		return statusDiagnostic{Severity: severityWarning, Code: codeWorktreeOutsideRoots, Message: reported.Path + " is outside allowed roots", Recovery: "add an allowed root or remove the worktree"}
	default:
		return statusDiagnostic{Severity: severityWarning, Code: codeWorktreePrunable, Message: reported.Path + " has prunable Git metadata", Recovery: "inspect with wts cleanup"}
	}
}

func statusDesiredWorktrees(snapshot gitclient.Snapshot, plan reconcile.Plan) []statusWorktree {
	ids := make(map[string]bool)
	for _, window := range plan.Desired.Windows {
		ids[window.Metadata.Identity] = true
	}
	result := make([]statusWorktree, 0)
	for _, worktree := range snapshot.Worktrees {
		if !ids[worktree.Identity] {
			continue
		}
		result = append(result, statusWorktree{Path: worktree.Path, HEAD: worktree.HEAD, Branch: worktree.Branch, Detached: worktree.Detached, Locked: worktree.Locked, Identity: worktree.Identity, Eligible: true})
	}
	return result
}

func statusActualWindows(snapshot tmux.Snapshot, repo config.Repository) []statusWindow {
	result := make([]statusWindow, 0)
	for _, session := range snapshot.Sessions {
		for _, window := range session.Windows {
			if tmux.ValidOwnedWindow(window.Metadata, repo.Identity()) {
				result = append(result, statusWindow{ID: window.ID, Name: window.Name, Path: window.Path, Role: window.Metadata.Role, Identity: window.Metadata.Identity})
			}
		}
	}
	return result
}

func actionFailure(repo config.Repository, key state.ActionKey, result state.ActionResult, paths map[string]string) statusActionFailure {
	action := actions.DefinitionKind(repo, key.Digest)
	worktreePath := paths[key.Worktree]
	target := worktreePath
	if target == "" {
		target = key.Worktree
	}
	errorCode := "legacy_failure"
	message := fmt.Sprintf("recorded legacy or obsolete action failure; run wts reconcile --repo-id %s to prune obsolete attempts", repo.ID)
	switch action {
	case "setup":
		errorCode = "setup_execution_failed"
		message = fmt.Sprintf("recorded setup failure; after correcting the cause run wts worktree setup %q --repo-id %s --rerun", target, repo.ID)
	case "launch":
		errorCode = "launch_delivery_failed"
		message = fmt.Sprintf("recorded launch failure; after correcting the cause run wts worktree launch %q --repo-id %s --rerun", target, repo.ID)
	}
	return statusActionFailure{WorktreeIdentity: key.Worktree, WorktreePath: worktreePath, Action: action, Attempted: result.Attempted.UTC().Format(time.RFC3339Nano), ErrorCode: errorCode, Message: message}
}

func repositoryHealth(entry statusRepository) statusHealth {
	if len(entry.Conflicts) > 0 {
		return healthConflict
	}
	for _, diagnostic := range entry.Diagnostics {
		if diagnostic.Severity == severityError {
			return healthDegraded
		}
	}
	if len(entry.Diagnostics) > 0 || len(entry.ReportedWorktrees) > 0 || len(entry.PrunableWorktrees) > 0 || len(entry.ActionFailures) > 0 {
		return healthAttention
	}
	return healthHealthy
}

func sortStatusDocument(document *statusDocument) {
	sort.Slice(document.Repositories, func(i, j int) bool { return document.Repositories[i].ID < document.Repositories[j].ID })
	sortDiagnostics := func(values []statusDiagnostic) {
		sort.Slice(values, func(i, j int) bool {
			if values[i].Severity != values[j].Severity {
				return values[i].Severity < values[j].Severity
			}
			if values[i].Code != values[j].Code {
				return values[i].Code < values[j].Code
			}
			return values[i].Message < values[j].Message
		})
	}
	sortDiagnostics(document.Diagnostics)
	for i := range document.Repositories {
		repo := &document.Repositories[i]
		sortDiagnostics(repo.Diagnostics)
		sort.Slice(repo.DesiredWorktrees, func(i, j int) bool {
			if repo.DesiredWorktrees[i].Path != repo.DesiredWorktrees[j].Path {
				return repo.DesiredWorktrees[i].Path < repo.DesiredWorktrees[j].Path
			}
			return repo.DesiredWorktrees[i].Identity < repo.DesiredWorktrees[j].Identity
		})
		sort.Slice(repo.ActualManagedWindows, func(i, j int) bool { return repo.ActualManagedWindows[i].ID < repo.ActualManagedWindows[j].ID })
		sort.Strings(repo.Conflicts)
		sort.Slice(repo.ReportedWorktrees, func(i, j int) bool {
			if repo.ReportedWorktrees[i].Path != repo.ReportedWorktrees[j].Path {
				return repo.ReportedWorktrees[i].Path < repo.ReportedWorktrees[j].Path
			}
			return repo.ReportedWorktrees[i].Reason < repo.ReportedWorktrees[j].Reason
		})
		sort.Slice(repo.PrunableWorktrees, func(i, j int) bool {
			if repo.PrunableWorktrees[i].Path != repo.PrunableWorktrees[j].Path {
				return repo.PrunableWorktrees[i].Path < repo.PrunableWorktrees[j].Path
			}
			return repo.PrunableWorktrees[i].Reason < repo.PrunableWorktrees[j].Reason
		})
		sort.Slice(repo.ActionFailures, func(i, j int) bool {
			if repo.ActionFailures[i].Action != repo.ActionFailures[j].Action {
				return repo.ActionFailures[i].Action < repo.ActionFailures[j].Action
			}
			if repo.ActionFailures[i].WorktreeIdentity != repo.ActionFailures[j].WorktreeIdentity {
				return repo.ActionFailures[i].WorktreeIdentity < repo.ActionFailures[j].WorktreeIdentity
			}
			return repo.ActionFailures[i].Attempted < repo.ActionFailures[j].Attempted
		})
	}
}

func (s *Service) statusDaemon(ctx context.Context) (statusDaemon, []statusDiagnostic) {
	if runtime.GOOS != "darwin" {
		return statusDaemon{State: statusDaemonUnsup}, []statusDiagnostic{}
	}
	output, err := s.daemon(ctx, "status", nil)
	if err != nil {
		return statusDaemon{State: statusDaemonUnavail, Message: "LaunchAgent state is unavailable"}, []statusDiagnostic{{Severity: severityError, Code: codeDaemonUnavailable, Message: "LaunchAgent state is unavailable", Recovery: "run wts daemon status or wts daemon logs"}}
	}
	switch {
	case strings.Contains(output, "running"):
		return statusDaemon{State: statusDaemonRunning}, []statusDiagnostic{}
	case strings.Contains(output, "stopped"):
		return statusDaemon{State: statusDaemonStopped}, []statusDiagnostic{{Severity: severityWarning, Code: codeDaemonStopped, Message: "installed LaunchAgent is stopped", Recovery: "run wts daemon start"}}
	default:
		return statusDaemon{State: statusDaemonAbsent}, []statusDiagnostic{}
	}
}

func (s *Service) collectStatus(ctx context.Context, cfg config.Config, repos []config.Repository) (statusDocument, error) {
	document := newStatusDocument()
	document.Daemon, document.Diagnostics = s.statusDaemon(ctx)
	ledger, ledgerErr := state.LoadLedger(filepath.Join(s.paths.State, "actions.json"))
	attempts := make([]decodedActionAttempt, 0)
	if ledgerErr != nil {
		document.Diagnostics = append(document.Diagnostics, statusDiagnostic{Severity: severityError, Code: codeActionLedgerUnavailable, Message: "action ledger could not be read", Recovery: "repair or remove the private action ledger after backing it up"})
	} else {
		for encoded, result := range ledger.Attempts {
			key, err := state.DecodeActionKey(encoded)
			if err != nil {
				document.Diagnostics = append(document.Diagnostics, statusDiagnostic{Severity: severityError, Code: codeActionLedgerUnavailable, Message: "action ledger contains an invalid key", Recovery: "repair or remove the private action ledger after backing it up"})
				continue
			}
			attempts = append(attempts, decodedActionAttempt{key: key, result: result})
		}
	}
	git, tmuxClient, _, _, _, err := s.runtime(cfg)
	if err != nil {
		return statusDocument{}, err
	}
	tmuxSnapshot, tmuxErr := tmuxClient.Snapshot(ctx)
	for _, repo := range repos {
		entry := statusRepository{ID: repo.ID, Health: healthHealthy, Diagnostics: []statusDiagnostic{}, DesiredWorktrees: []statusWorktree{}, ActualManagedWindows: []statusWindow{}, Conflicts: []string{}, ReportedWorktrees: []statusReportedWorktree{}, PrunableWorktrees: []statusReportedWorktree{}, ActionFailures: []statusActionFailure{}}
		if tmuxErr != nil {
			entry.Diagnostics = append(entry.Diagnostics, statusDiagnostic{Severity: severityError, Code: codeTmuxSnapshotFailed, Message: "tmux snapshot failed", Recovery: "check tmux availability and the dedicated wts socket"})
		} else if !tmuxSnapshot.Complete {
			entry.Diagnostics = append(entry.Diagnostics, statusDiagnostic{Severity: severityError, Code: codeTmuxSnapshotIncomplete, Message: "tmux snapshot is incomplete", Recovery: "retry status after tmux becomes available"})
		}
		gitSnapshot, gitErr := git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir})
		switch {
		case !gitSnapshot.Complete && len(gitSnapshot.Worktrees) > 0:
			entry.Diagnostics = append(entry.Diagnostics, statusDiagnostic{Severity: severityError, Code: codeGitSnapshotIncomplete, Message: "Git worktree snapshot is incomplete", Recovery: "repair missing paths or linked-worktree metadata"})
		case gitErr != nil:
			entry.Diagnostics = append(entry.Diagnostics, statusDiagnostic{Severity: severityError, Code: codeGitSnapshotFailed, Message: "Git worktree snapshot failed", Recovery: "inspect the registered primary worktree and Git metadata"})
		case !gitSnapshot.Complete:
			entry.Diagnostics = append(entry.Diagnostics, statusDiagnostic{Severity: severityError, Code: codeGitSnapshotIncomplete, Message: "Git worktree snapshot is incomplete", Recovery: "repair missing paths or linked-worktree metadata"})
		}
		plan := reconcile.Build(repo, gitSnapshot, tmuxSnapshot)
		entry.DesiredWorktrees = statusDesiredWorktrees(gitSnapshot, plan)
		entry.ActualManagedWindows = statusActualWindows(tmuxSnapshot, repo)
		entry.Conflicts = append(entry.Conflicts, plan.Conflicts...)
		for i, conflict := range plan.Conflicts {
			code := codeSessionNameConflict
			if i < len(plan.ConflictCodes) && plan.ConflictCodes[i] == codeMultipleOwnedSessions {
				code = codeMultipleOwnedSessions
			}
			entry.Diagnostics = append(entry.Diagnostics, statusDiagnostic{Severity: severityError, Code: code, Message: conflict, Recovery: "resolve the tmux ownership conflict before reconciling"})
		}
		paths := make(map[string]string)
		for _, worktree := range gitSnapshot.Worktrees {
			paths[worktree.Identity] = worktree.Path
		}
		for _, reported := range plan.Reported {
			item := statusReportedWorktree{Path: reported.Path, Reason: reported.Reason}
			if reported.Reason == reasonGitPrunable {
				entry.PrunableWorktrees = append(entry.PrunableWorktrees, item)
			} else {
				entry.ReportedWorktrees = append(entry.ReportedWorktrees, item)
			}
			entry.Diagnostics = append(entry.Diagnostics, statusDiagnosticForReported(reported))
		}
		for _, attempt := range attempts {
			if attempt.key.Repository == repo.Identity() && !attempt.result.Success {
				entry.ActionFailures = append(entry.ActionFailures, actionFailure(repo, attempt.key, attempt.result, paths))
			}
		}
		entry.Health = repositoryHealth(entry)
		document.Repositories = append(document.Repositories, entry)
	}
	sortStatusDocument(&document)
	return document, nil
}

func (s *Service) status(ctx context.Context, repoID string, all, jsonOutput, verbose, check bool) (string, error) {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return "", err
	}
	repos := cfg.Repositories
	if !all {
		repo, resolveErr := s.resolveRepo(ctx, cfg, repoID)
		if resolveErr != nil {
			return "", resolveErr
		}
		repos = []config.Repository{repo}
	}
	document, err := s.collectStatus(ctx, cfg, repos)
	if err != nil {
		return "", err
	}
	var output string
	if jsonOutput {
		data, marshalErr := json.MarshalIndent(document, "", "  ")
		if marshalErr != nil {
			return "", marshalErr
		}
		output = string(data)
	} else {
		output = renderStatusHuman(document, verbose)
	}
	if check && statusRequiresAttention(document) {
		return output, fmt.Errorf("status check failed: attention is required")
	}
	return output, nil
}
