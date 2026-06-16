package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pddispatcher "github.com/averycrespi/agent-tools/pi-dispatcher/pkg/dispatcher"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestCleanupDryRunShowsTerminalRunTargetsWithoutRemoving(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	fakeWT := installFakeWorktreeRemover(t)
	paths := seedCleanupWorkflowRun(t, stateDir, store.StateSucceeded)

	stdout, err := executeCommand("cleanup", "--dry-run", "run-1")
	if err != nil {
		t.Fatalf("cleanup dry-run error = %v", err)
	}
	if !strings.Contains(stdout, paths.worktree) || !strings.Contains(stdout, paths.artifacts) {
		t.Fatalf("stdout = %q, want cleanup targets", stdout)
	}
	if _, err := os.Stat(paths.worktree); err != nil {
		t.Fatalf("worktree stat after dry-run: %v", err)
	}
	if fakeWT.called {
		t.Fatal("worktree remover called during dry-run")
	}
}

func TestCleanupRemovesTerminalRunWorktreeAndArtifacts(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	fakeWT := installFakeWorktreeRemover(t)
	paths := seedCleanupWorkflowRun(t, stateDir, store.StateSucceeded)
	fakeWT.remove = func(_, _ string) error { return os.RemoveAll(paths.worktree) }

	if _, err := executeCommand("cleanup", "run-1"); err != nil {
		t.Fatalf("cleanup error = %v", err)
	}
	if _, err := os.Stat(paths.worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(paths.artifacts); !os.IsNotExist(err) {
		t.Fatalf("artifacts stat error = %v, want not exist", err)
	}
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	detail, err := db.GetWorkflowRunDetail(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRunDetail() error = %v", err)
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].Exists {
		t.Fatalf("artifacts = %+v, want metadata marked removed", detail.Artifacts)
	}
	if detail.Run.CleanupStatus != "removed" || detail.Run.CleanupError != "" || !detail.Run.CleanupAttemptedAt.Valid {
		t.Fatalf("run cleanup metadata = status %q error %q attempted %v, want removed", detail.Run.CleanupStatus, detail.Run.CleanupError, detail.Run.CleanupAttemptedAt)
	}
	if !fakeWT.called || fakeWT.repoRoot != "/repo" || fakeWT.branch != "po/run-1" {
		t.Fatalf("worktree remover = %+v, want persisted repo and branch", fakeWT)
	}
}

func TestCleanupDelegatesBackingPDTaskCleanup(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	fakeWT := installFakeWorktreeRemover(t)
	fakePD := installFakePDCleanupClient(t)
	paths := seedCleanupWorkflowRunWithPDTask(t, stateDir, "run-1", store.StateSucceeded, "pd-task-1", "pd-task-1-run-1")

	if _, err := executeCommand("cleanup", "run-1"); err != nil {
		t.Fatalf("cleanup error = %v", err)
	}
	if fakeWT.called {
		t.Fatal("direct worktree remover called despite backing pd task")
	}
	if len(fakePD.calls) != 1 || fakePD.calls[0].TaskID != "pd-task-1" || fakePD.calls[0].RunID != "pd-task-1-run-1" {
		t.Fatalf("pd cleanup calls = %+v, want backing task cleanup", fakePD.calls)
	}
	if _, err := os.Stat(paths.artifacts); !os.IsNotExist(err) {
		t.Fatalf("artifacts stat error = %v, want not exist", err)
	}
}

func TestCleanupAcceptsMultipleRunIDs(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	fakeWT := installFakeWorktreeRemover(t)
	first := seedCleanupWorkflowRunWithID(t, stateDir, "run-1", store.StateSucceeded)
	second := seedCleanupWorkflowRunWithID(t, stateDir, "run-2", store.StateSucceeded)
	worktrees := map[string]string{"po/run-1": first.worktree, "po/run-2": second.worktree}
	fakeWT.remove = func(_, branch string) error { return os.RemoveAll(worktrees[branch]) }

	stdout, err := executeCommand("cleanup", "run-1", "run-2")
	if err != nil {
		t.Fatalf("cleanup error = %v", err)
	}
	if !strings.Contains(stdout, "cleaned run run-1") || !strings.Contains(stdout, "cleaned run run-2") {
		t.Fatalf("stdout = %q, want both cleanup messages", stdout)
	}
	if len(fakeWT.calls) != 2 || fakeWT.calls[0].branch != "po/run-1" || fakeWT.calls[1].branch != "po/run-2" {
		t.Fatalf("worktree calls = %+v, want both runs", fakeWT.calls)
	}
	for _, path := range []string{first.worktree, first.artifacts, second.worktree, second.artifacts} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stat %s error = %v, want not exist", path, err)
		}
	}
}

func TestCleanupAllCleansTerminalRunsOnly(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	fakeWT := installFakeWorktreeRemover(t)
	first := seedCleanupWorkflowRunWithID(t, stateDir, "run-1", store.StateSucceeded)
	second := seedCleanupWorkflowRunWithID(t, stateDir, "run-2", store.StateFailed)
	unknown := seedCleanupWorkflowRunWithID(t, stateDir, "run-unknown", store.StateUnknown)
	active := seedCleanupWorkflowRunWithID(t, stateDir, "run-3", store.StateRunning)
	worktrees := map[string]string{"po/run-1": first.worktree, "po/run-2": second.worktree}
	fakeWT.remove = func(_, branch string) error { return os.RemoveAll(worktrees[branch]) }

	stdout, err := executeCommand("cleanup", "--all")
	if err != nil {
		t.Fatalf("cleanup --all error = %v", err)
	}
	if !strings.Contains(stdout, "cleaned run run-1") || !strings.Contains(stdout, "cleaned run run-2") || !strings.Contains(stdout, "skipped run run-unknown") || strings.Contains(stdout, "cleaned run run-3") {
		t.Fatalf("stdout = %q, want terminal runs only with unknown skipped", stdout)
	}
	if len(fakeWT.calls) != 2 {
		t.Fatalf("worktree calls = %+v, want two terminal runs", fakeWT.calls)
	}
	for _, path := range []string{first.worktree, first.artifacts, second.worktree, second.artifacts} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stat %s error = %v, want not exist", path, err)
		}
	}
	for _, path := range []string{unknown.worktree, unknown.artifacts, active.worktree, active.artifacts} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat preserved path %s after cleanup --all: %v", path, err)
		}
	}
}

func TestCleanupAllIncludesUnknownWithFlag(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	fakeWT := installFakeWorktreeRemover(t)
	unknown := seedCleanupWorkflowRunWithID(t, stateDir, "run-unknown", store.StateUnknown)
	fakeWT.remove = func(_, branch string) error {
		if branch == "po/run-unknown" {
			return os.RemoveAll(unknown.worktree)
		}
		return nil
	}

	stdout, err := executeCommand("cleanup", "--all", "--include-unknown")
	if err != nil {
		t.Fatalf("cleanup --all --include-unknown error = %v", err)
	}
	if !strings.Contains(stdout, "cleaned run run-unknown") {
		t.Fatalf("stdout = %q, want unknown run cleaned", stdout)
	}
	if len(fakeWT.calls) != 1 || fakeWT.calls[0].branch != "po/run-unknown" {
		t.Fatalf("worktree calls = %+v, want unknown run", fakeWT.calls)
	}
}

func TestCleanupAllRejectsRunIDs(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedCleanupWorkflowRun(t, stateDir, store.StateSucceeded)

	_, err := executeCommand("cleanup", "--all", "run-1")
	if err == nil || !strings.Contains(err.Error(), "--all cannot be used with run IDs") {
		t.Fatalf("cleanup --all run-1 error = %v, want mixed arg rejection", err)
	}
}

func TestCleanupRejectsNonTerminalRun(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	installFakeWorktreeRemover(t)
	supervisorProcessAlive = func(int) bool { return true }
	t.Cleanup(func() { supervisorProcessAlive = defaultSupervisorProcessAlive })
	seedCleanupWorkflowRun(t, stateDir, store.StateRunning)

	_, err := executeCommand("cleanup", "run-1")
	if err == nil || !strings.Contains(err.Error(), "workflow run run-1 is not terminal") {
		t.Fatalf("cleanup error = %v, want non-terminal error", err)
	}
}

func TestCleanupRejectsArtifactRootOutsideConfiguredParent(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	fakeWT := installFakeWorktreeRemover(t)
	paths := seedCleanupWorkflowRunWithArtifactRoot(t, stateDir, store.StateSucceeded, filepath.Join(t.TempDir(), "outside-artifacts"))

	_, err := executeCommand("cleanup", "run-1")
	if err == nil || !strings.Contains(err.Error(), "is outside configured artifact parent") {
		t.Fatalf("cleanup error = %v, want artifact parent boundary error", err)
	}
	if fakeWT.called {
		t.Fatal("worktree remover called before artifact root validation")
	}
	if _, statErr := os.Stat(paths.worktree); statErr != nil {
		t.Fatalf("worktree stat after rejected cleanup: %v", statErr)
	}
	if _, statErr := os.Stat(paths.artifacts); statErr != nil {
		t.Fatalf("artifacts stat after rejected cleanup: %v", statErr)
	}
}

func TestRMRejectsNonTerminalRun(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	supervisorProcessAlive = func(int) bool { return true }
	t.Cleanup(func() { supervisorProcessAlive = defaultSupervisorProcessAlive })
	seedCleanupWorkflowRun(t, stateDir, store.StateRunning)

	_, err := executeCommand("rm", "run-1")
	if err == nil || !strings.Contains(err.Error(), "workflow run run-1 is not terminal") {
		t.Fatalf("rm error = %v, want non-terminal error", err)
	}
}

func TestRMAcceptsMultipleRunIDs(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	first := seedCleanupWorkflowRunWithID(t, stateDir, "run-1", store.StateSucceeded)
	second := seedCleanupWorkflowRunWithID(t, stateDir, "run-2", store.StateSucceeded)

	stdout, err := executeCommand("rm", "run-1", "run-2")
	if err != nil {
		t.Fatalf("rm error = %v", err)
	}
	if !strings.Contains(stdout, "removed run run-1") || !strings.Contains(stdout, "removed run run-2") {
		t.Fatalf("stdout = %q, want both rm messages", stdout)
	}
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	for _, runID := range []string{"run-1", "run-2"} {
		if _, err := db.GetWorkflowRun(context.Background(), runID); err == nil {
			t.Fatalf("GetWorkflowRun(%s) error = nil, want removed metadata", runID)
		}
	}
	for _, path := range []string{first.worktree, first.artifacts, second.worktree, second.artifacts} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s after rm: %v", path, err)
		}
	}
}

func TestRMAllDeletesTerminalRunMetadataOnly(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	first := seedCleanupWorkflowRunWithID(t, stateDir, "run-1", store.StateSucceeded)
	second := seedCleanupWorkflowRunWithID(t, stateDir, "run-2", store.StateFailed)
	unknown := seedCleanupWorkflowRunWithID(t, stateDir, "run-unknown", store.StateUnknown)
	active := seedCleanupWorkflowRunWithID(t, stateDir, "run-3", store.StateRunning)

	stdout, err := executeCommand("rm", "--all")
	if err != nil {
		t.Fatalf("rm --all error = %v", err)
	}
	if !strings.Contains(stdout, "removed run run-1") || !strings.Contains(stdout, "removed run run-2") || !strings.Contains(stdout, "skipped run run-unknown") || strings.Contains(stdout, "removed run run-3") {
		t.Fatalf("stdout = %q, want terminal runs only with unknown skipped", stdout)
	}
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	for _, runID := range []string{"run-1", "run-2"} {
		if _, err := db.GetWorkflowRun(context.Background(), runID); err == nil {
			t.Fatalf("GetWorkflowRun(%s) error = nil, want removed metadata", runID)
		}
	}
	for _, runID := range []string{"run-unknown", "run-3"} {
		if _, err := db.GetWorkflowRun(context.Background(), runID); err != nil {
			t.Fatalf("GetWorkflowRun(%s) error = %v, want run preserved", runID, err)
		}
	}
	for _, path := range []string{first.worktree, first.artifacts, second.worktree, second.artifacts, unknown.worktree, unknown.artifacts, active.worktree, active.artifacts} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s after rm --all: %v", path, err)
		}
	}
}

func TestRMAllIncludesUnknownWithFlag(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedCleanupWorkflowRunWithID(t, stateDir, "run-unknown", store.StateUnknown)

	stdout, err := executeCommand("rm", "--all", "--include-unknown")
	if err != nil {
		t.Fatalf("rm --all --include-unknown error = %v", err)
	}
	if !strings.Contains(stdout, "removed run run-unknown") {
		t.Fatalf("stdout = %q, want unknown run removed", stdout)
	}
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.GetWorkflowRun(context.Background(), "run-unknown"); err == nil {
		t.Fatal("GetWorkflowRun(run-unknown) error = nil, want removed metadata")
	}
}

func TestRMAllRejectsRunIDs(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedCleanupWorkflowRun(t, stateDir, store.StateSucceeded)

	_, err := executeCommand("rm", "--all", "run-1")
	if err == nil || !strings.Contains(err.Error(), "--all cannot be used with run IDs") {
		t.Fatalf("rm --all run-1 error = %v, want mixed arg rejection", err)
	}
}

func TestRMDelegatesBackingPDTaskRemoval(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	fakePD := installFakePDRemoveClient(t)
	seedCleanupWorkflowRunWithPDTask(t, stateDir, "run-1", store.StateSucceeded, "pd-task-1", "pd-task-1-run-1")

	if _, err := executeCommand("rm", "run-1"); err != nil {
		t.Fatalf("rm error = %v", err)
	}
	if len(fakePD.calls) != 1 || fakePD.calls[0].TaskID != "pd-task-1" || fakePD.calls[0].RunID != "pd-task-1-run-1" {
		t.Fatalf("pd remove calls = %+v, want backing task removal", fakePD.calls)
	}
}

func TestRMIsIdempotentForMissingRun(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)

	stdout, err := executeCommand("rm", "run-missing")
	if err != nil {
		t.Fatalf("rm missing run error = %v", err)
	}
	if !strings.Contains(stdout, "removed run run-missing\talready_missing=true") {
		t.Fatalf("stdout = %q, want idempotent removed message", stdout)
	}
}

func TestRMDeletesTerminalRunMetadata(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	paths := seedCleanupWorkflowRun(t, stateDir, store.StateSucceeded)

	if _, err := executeCommand("rm", "run-1"); err != nil {
		t.Fatalf("rm error = %v", err)
	}
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.GetWorkflowRun(context.Background(), "run-1"); err == nil {
		t.Fatal("GetWorkflowRun() error = nil, want removed metadata")
	}
	if _, err := os.Stat(paths.worktree); err != nil {
		t.Fatalf("worktree stat after rm: %v", err)
	}
	if _, err := os.Stat(paths.artifacts); err != nil {
		t.Fatalf("artifacts stat after rm: %v", err)
	}
}

type cleanupPaths struct {
	worktree  string
	artifacts string
}

type fakeWorktreeRemoveCall struct {
	repoRoot string
	branch   string
}

type fakeWorktreeRemover struct {
	called   bool
	repoRoot string
	branch   string
	calls    []fakeWorktreeRemoveCall
	remove   func(repoRoot, branch string) error
}

type fakePDCleanupClient struct {
	calls []pddispatcher.CleanupTaskRunRequest
	err   error
}

type fakePDRemoveClient struct {
	calls []pddispatcher.RemoveTaskRunRequest
	err   error
}

func installFakePDCleanupClient(t *testing.T) *fakePDCleanupClient {
	t.Helper()
	fake := &fakePDCleanupClient{}
	old := newPDCleanupClient
	newPDCleanupClient = func() pdCleanupClient { return fake }
	t.Cleanup(func() { newPDCleanupClient = old })
	return fake
}

func (f *fakePDCleanupClient) CleanupTaskRun(_ context.Context, req pddispatcher.CleanupTaskRunRequest) (pddispatcher.CleanupTaskRunResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return pddispatcher.CleanupTaskRunResult{}, f.err
	}
	return pddispatcher.CleanupTaskRunResult{TaskID: req.TaskID, RunID: req.RunID, Status: "removed", RemovedWorktree: true}, nil
}

func installFakePDRemoveClient(t *testing.T) *fakePDRemoveClient {
	t.Helper()
	fake := &fakePDRemoveClient{}
	old := newPDRemoveClient
	newPDRemoveClient = func() pdRemoveClient { return fake }
	t.Cleanup(func() { newPDRemoveClient = old })
	return fake
}

func (f *fakePDRemoveClient) RemoveTaskRun(_ context.Context, req pddispatcher.RemoveTaskRunRequest) (pddispatcher.RemoveTaskRunResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return pddispatcher.RemoveTaskRunResult{}, f.err
	}
	return pddispatcher.RemoveTaskRunResult{TaskID: req.TaskID, RunID: req.RunID, Removed: true}, nil
}

func installFakeWorktreeRemover(t *testing.T) *fakeWorktreeRemover {
	t.Helper()
	fake := &fakeWorktreeRemover{}
	old := newWorktreeRemover
	newWorktreeRemover = func() (worktreeRemover, error) { return fake, nil }
	t.Cleanup(func() { newWorktreeRemover = old })
	return fake
}

func (f *fakeWorktreeRemover) Remove(repoRoot, branch string) error {
	f.called = true
	f.repoRoot = repoRoot
	f.branch = branch
	f.calls = append(f.calls, fakeWorktreeRemoveCall{repoRoot: repoRoot, branch: branch})
	if f.remove != nil {
		return f.remove(repoRoot, branch)
	}
	return nil
}

func seedCleanupWorkflowRun(t *testing.T, stateDir string, state store.State) cleanupPaths {
	t.Helper()
	return seedCleanupWorkflowRunWithArtifactRoot(t, stateDir, state, filepath.Join(cfg.ArtifactParentDir, "run-1"))
}

func seedCleanupWorkflowRunWithID(t *testing.T, stateDir string, runID string, state store.State) cleanupPaths {
	t.Helper()
	return seedCleanupWorkflowRunWithIDAndArtifactRoot(t, stateDir, runID, state, filepath.Join(cfg.ArtifactParentDir, runID))
}

func seedCleanupWorkflowRunWithArtifactRoot(t *testing.T, stateDir string, state store.State, artifacts string) cleanupPaths {
	t.Helper()
	return seedCleanupWorkflowRunWithIDAndArtifactRoot(t, stateDir, "run-1", state, artifacts)
}

func seedCleanupWorkflowRunWithPDTask(t *testing.T, stateDir string, runID string, state store.State, pdTaskID string, pdRunID string) cleanupPaths {
	t.Helper()
	return seedCleanupWorkflowRunWithIDAndArtifactRootAndPDTask(t, stateDir, runID, state, filepath.Join(cfg.ArtifactParentDir, runID), pdTaskID, pdRunID)
}

func seedCleanupWorkflowRunWithIDAndArtifactRoot(t *testing.T, stateDir string, runID string, state store.State, artifacts string) cleanupPaths {
	t.Helper()
	return seedCleanupWorkflowRunWithIDAndArtifactRootAndPDTask(t, stateDir, runID, state, artifacts, "", "")
}

func seedCleanupWorkflowRunWithIDAndArtifactRootAndPDTask(t *testing.T, stateDir string, runID string, state store.State, artifacts string, pdTaskID string, pdRunID string) cleanupPaths {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	for _, path := range []string{worktree, artifacts} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	reqID := "req-" + runID
	req := store.RunRequest{ID: reqID, Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: runID, RequestID: reqID, Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/" + runID, WorktreePath: worktree, ArtifactRoot: artifacts, State: state, SupervisorLogPath: filepath.Join(root, "supervisor.log"), CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	artifactPath := filepath.Join(artifacts, "out.md")
	if err := os.WriteFile(artifactPath, []byte("out"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	step := store.StepRun{WorkflowRunID: runID, StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: state, PDTaskID: pdTaskID, PDRunID: pdRunID, StartedAt: now, UpdatedAt: now}
	artifact := store.Artifact{WorkflowRunID: runID, Name: "out", RelativePath: "out.md", AbsolutePath: artifactPath, Exists: true, UpdatedAt: now}
	if err := db.CreateStepRun(context.Background(), step); err != nil {
		t.Fatalf("create step: %v", err)
	}
	if err := db.UpsertArtifacts(context.Background(), []store.Artifact{artifact}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	return cleanupPaths{worktree: worktree, artifacts: artifacts}
}
