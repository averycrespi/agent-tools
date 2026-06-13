package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	fakeWT.remove = func() error { return os.RemoveAll(paths.worktree) }

	if _, err := executeCommand("cleanup", "run-1"); err != nil {
		t.Fatalf("cleanup error = %v", err)
	}
	if _, err := os.Stat(paths.worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(paths.artifacts); !os.IsNotExist(err) {
		t.Fatalf("artifacts stat error = %v, want not exist", err)
	}
	if !fakeWT.called || fakeWT.repoRoot != "/repo" || fakeWT.branch != "po/sample" {
		t.Fatalf("worktree remover = %+v, want persisted repo and branch", fakeWT)
	}
}

func TestCleanupRejectsNonTerminalRun(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	installFakeWorktreeRemover(t)
	seedCleanupWorkflowRun(t, stateDir, store.StateRunning)

	_, err := executeCommand("cleanup", "run-1")
	if err == nil || !strings.Contains(err.Error(), "workflow run run-1 is not terminal") {
		t.Fatalf("cleanup error = %v, want non-terminal error", err)
	}
}

func TestRMDeletesTerminalRunMetadata(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedCleanupWorkflowRun(t, stateDir, store.StateSucceeded)

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
}

type cleanupPaths struct {
	worktree  string
	artifacts string
}

type fakeWorktreeRemover struct {
	called   bool
	repoRoot string
	branch   string
	remove   func() error
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
	if f.remove != nil {
		return f.remove()
	}
	return nil
}

func seedCleanupWorkflowRun(t *testing.T, stateDir string, state store.State) cleanupPaths {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	artifacts := filepath.Join(root, "artifacts")
	for _, path := range []string{worktree, artifacts} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	req := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: "req-1", Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: worktree, ArtifactRoot: artifacts, State: state, SupervisorLogPath: filepath.Join(root, "supervisor.log"), CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return cleanupPaths{worktree: worktree, artifacts: artifacts}
}
