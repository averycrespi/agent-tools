package dispatcher

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	pdstore "github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
)

func TestStartTaskRunUsesCallerOwnedWorktree(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	launcher := &recordingLauncher{pid: 1234}
	client := NewClient(Config{DBPath: dbPath, StateDir: filepath.Join(t.TempDir(), "state"), RuntimeDir: filepath.Join(t.TempDir(), "runtime")})
	client.now = func() time.Time { return now }
	client.shortID = func() string { return "abcd" }
	client.launcher = launcher

	result, err := client.StartTaskRun(context.Background(), StartTaskRunRequest{
		RepoPath:     "/repo",
		RepoName:     "repo",
		Branch:       "po/workflow-abcd",
		WorktreePath: "/worktrees/workflow-abcd",
		Prompt:       "do the step",
		Agent: AgentOptions{
			Model:  "gpt-5.1-codex",
			Skills: []string{"review"},
		},
	})
	if err != nil {
		t.Fatalf("StartTaskRun() error = %v", err)
	}
	if result.TaskID != "pd-20260613-120000-abcd" || result.RunID != "pd-20260613-120000-abcd-run-1" {
		t.Fatalf("result = %+v, want deterministic task/run ids", result)
	}
	if !reflect.DeepEqual(launcher.args, []string{"--task-id", result.TaskID, "--pi-argv", launcher.args[3], "--env-names", "[]"}) {
		t.Fatalf("launcher args = %#v", launcher.args)
	}

	st, err := pdstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close() //nolint:errcheck
	task, err := st.GetTask(context.Background(), result.TaskID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if task.WorktreeCreatedByPD {
		t.Fatal("WorktreeCreatedByPD = true, want false for caller-owned worktree")
	}
	if task.WorktreePath != "/worktrees/workflow-abcd" || task.WorktreeCleanupPolicy != pdstore.CleanupPolicyNever {
		t.Fatalf("task = %+v, want caller-owned worktree with no cleanup", task)
	}
	run, err := st.LatestRun(context.Background(), result.TaskID)
	if err != nil {
		t.Fatalf("LatestRun() error = %v", err)
	}
	if run.ID != result.RunID || run.SupervisorPID != 1234 || run.Status != pdstore.StatusStarting {
		t.Fatalf("run = %+v, want starting run with supervisor pid", run)
	}
}

func TestStartTaskRunRejectsMissingCallerOwnedWorktree(t *testing.T) {
	t.Parallel()
	client := NewClient(Config{DBPath: filepath.Join(t.TempDir(), "pd.db"), StateDir: t.TempDir(), RuntimeDir: t.TempDir()})
	_, err := client.StartTaskRun(context.Background(), StartTaskRunRequest{RepoPath: "/repo", Branch: "branch", Prompt: "prompt"})
	if err == nil {
		t.Fatal("StartTaskRun() error = nil, want validation error")
	}
}

type recordingLauncher struct {
	pid  int
	env  []string
	args []string
}

func (r *recordingLauncher) StartSupervisorWithEnv(env []string, args ...string) (int, error) {
	r.env = append([]string(nil), env...)
	r.args = append([]string(nil), args...)
	return r.pid, nil
}
