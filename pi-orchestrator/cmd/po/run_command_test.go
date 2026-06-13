package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/config"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestRunCommandValidatesInputsBeforeCreatingWorktree(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "review", runWorkflowYAML("review"))
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, dir, stateDir)
	validateArtifactParent = func(string) error { return nil }
	calls := 0
	newWorktreeClient = func() (worktreeClient, error) {
		calls++
		return fakeWorktreeClient{path: "/worktree/review"}, nil
	}
	t.Cleanup(resetRunTestHooks)

	_, err := executeCommand("--workflow-dir", dir, "run", "review", "--input", "repo=/repo")
	if err == nil {
		t.Fatal("run error = nil, want missing input error")
	}
	if !strings.Contains(err.Error(), "missing required input pr_number") {
		t.Fatalf("error = %q, want missing pr_number", err.Error())
	}
	if calls != 0 {
		t.Fatalf("worktree calls = %d, want 0", calls)
	}
}

func TestRunCommandRejectsWorkflowPathTraversalBeforeCreatingWorktree(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, dir, stateDir)
	artifactChecks := 0
	validateArtifactParent = func(string) error {
		artifactChecks++
		return nil
	}
	worktreeCalls := 0
	newWorktreeClient = func() (worktreeClient, error) {
		worktreeCalls++
		return fakeWorktreeClient{path: "/worktree/review"}, nil
	}
	t.Cleanup(resetRunTestHooks)

	_, err := executeCommand("--workflow-dir", dir, "run", "../review", "--input", "repo=/repo", "--input", "pr_number=42")
	if err == nil || !strings.Contains(err.Error(), "workflow name must not contain path separators") {
		t.Fatalf("run error = %v, want path separator rejection", err)
	}
	if artifactChecks != 0 || worktreeCalls != 0 {
		t.Fatalf("artifact checks = %d worktree calls = %d, want both 0", artifactChecks, worktreeCalls)
	}
}

func TestRunCommandRejectsInvisibleArtifactParentBeforeCreatingWorktree(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "review", runWorkflowYAML("review"))
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, dir, stateDir)
	validateArtifactParent = func(path string) error {
		if path != cfg.ArtifactParentDir {
			t.Fatalf("artifact parent = %q, want %q", path, cfg.ArtifactParentDir)
		}
		return fmt.Errorf("not mounted")
	}
	calls := 0
	newWorktreeClient = func() (worktreeClient, error) {
		calls++
		return fakeWorktreeClient{path: "/worktree/review"}, nil
	}
	t.Cleanup(resetRunTestHooks)

	_, err := executeCommand("--workflow-dir", dir, "run", "review", "--input", "repo=/repo", "--input", "pr_number=42")
	if err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("run error = %v, want artifact parent validation error", err)
	}
	if calls != 0 {
		t.Fatalf("worktree calls = %d, want 0", calls)
	}
}

func TestRunCommandCreatesWorkflowRunWithOneWorktree(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "review", runWorkflowYAML("review"))
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, dir, stateDir)
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }
	shortIDFunc = func() string { return "abcd" }
	validateArtifactParent = func(path string) error {
		if path != cfg.ArtifactParentDir {
			t.Fatalf("artifact parent = %q, want %q", path, cfg.ArtifactParentDir)
		}
		return nil
	}
	fakeWT := &recordingWorktreeClient{path: "/worktrees/po-review-abcd"}
	newWorktreeClient = func() (worktreeClient, error) { return fakeWT, nil }
	launcher := &recordingSupervisorLauncher{pid: 4321}
	startSupervisor = launcher.Start
	t.Cleanup(resetRunTestHooks)

	stdout, err := executeCommand("--workflow-dir", dir, "run", "review", "--input", "repo=/repo", "--input", "pr_number=42")
	if err != nil {
		t.Fatalf("run error = %v", err)
	}
	if strings.TrimSpace(stdout) != "po-20260613-120000-abcd" {
		t.Fatalf("stdout = %q, want run id", stdout)
	}
	if fakeWT.repoRoot != "/repo" || fakeWT.branch != "po/review-abcd" || fakeWT.calls != 1 {
		t.Fatalf("worktree call = repo %q branch %q calls %d", fakeWT.repoRoot, fakeWT.branch, fakeWT.calls)
	}

	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	run, err := db.GetWorkflowRun(context.Background(), "po-20260613-120000-abcd")
	if err != nil {
		t.Fatalf("GetWorkflowRun() error = %v", err)
	}
	if run.State != store.StateStarting || run.Workflow != "review" || run.Repo != "/repo" {
		t.Fatalf("run = %+v, want starting review for /repo", run)
	}
	if run.SupervisorPID != 4321 {
		t.Fatalf("SupervisorPID = %d, want 4321", run.SupervisorPID)
	}
	if launcher.logPath != run.SupervisorLogPath {
		t.Fatalf("supervisor log path = %q, want %q", launcher.logPath, run.SupervisorLogPath)
	}
	if len(launcher.args) != 2 || launcher.args[0] != "--run-id" || launcher.args[1] != run.ID {
		t.Fatalf("supervisor args = %#v, want --run-id", launcher.args)
	}
	if run.WorktreePath != "/worktrees/po-review-abcd" {
		t.Fatalf("WorktreePath = %q", run.WorktreePath)
	}
	if run.ArtifactRoot != filepath.Join(stateDir, "po", "artifacts", run.ID) {
		t.Fatalf("ArtifactRoot = %q", run.ArtifactRoot)
	}
}

func runWorkflowYAML(name string) string {
	return `name: ` + name + `
repo: "{{ .Inputs.repo }}"
inputs:
  repo:
    type: string
    required: true
  pr_number:
    type: integer
    required: true
agents:
  reviewer:
    model: gpt-5.1-codex
steps:
  - id: review
    agent: reviewer
    prompt: review
`
}

type fakeWorktreeClient struct{ path string }

func (f fakeWorktreeClient) AddHeadlessWithOwnership(string, string) (string, bool, error) {
	return f.path, true, nil
}

func (f fakeWorktreeClient) Remove(string, string) error { return nil }

type recordingWorktreeClient struct {
	path     string
	repoRoot string
	branch   string
	calls    int
}

func (r *recordingWorktreeClient) AddHeadlessWithOwnership(repoRoot, branch string) (string, bool, error) {
	r.calls++
	r.repoRoot = repoRoot
	r.branch = branch
	return r.path, true, nil
}

func (r *recordingWorktreeClient) Remove(string, string) error { return nil }

func testConfig(t *testing.T, workflowDir string, stateDir string) config.Config {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(filepath.Dir(workflowDir)))
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "runtime"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	cfg.WorkflowDir = workflowDir
	return cfg
}

type recordingSupervisorLauncher struct {
	pid     int
	logPath string
	args    []string
}

func (r *recordingSupervisorLauncher) Start(logPath string, args ...string) (int, error) {
	r.logPath = logPath
	r.args = append([]string(nil), args...)
	return r.pid, nil
}

func resetRunTestHooks() {
	newWorktreeClient = defaultNewWorktreeClient
	startSupervisor = defaultStartSupervisor
	validateArtifactParent = defaultValidateArtifactParent
	nowFunc = time.Now
	shortIDFunc = randomShortID
}
