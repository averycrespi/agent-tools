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
	if result.StdoutPath == "" || result.StderrPath == "" || result.PiEventsPath == "" {
		t.Fatalf("result = %+v, want log/event paths", result)
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

func TestGetTaskRunReturnsStateAndMetadata(t *testing.T) {
	t.Parallel()
	client, result := startedTaskRun(t)

	info, err := client.GetTaskRun(context.Background(), GetTaskRunRequest{TaskID: result.TaskID, RunID: result.RunID})
	if err != nil {
		t.Fatalf("GetTaskRun() error = %v", err)
	}
	if info.TaskID != result.TaskID || info.RunID != result.RunID || info.Status != TaskRunStatusStarting {
		t.Fatalf("info = %+v, want starting task/run", info)
	}
	if info.StdoutLogPath == "" || info.StderrLogPath == "" || info.PiEventsPath == "" || info.ControlSocketPath == "" {
		t.Fatalf("info = %+v, want log/event/control metadata", info)
	}
}

func TestWaitTaskRunReturnsTerminalState(t *testing.T) {
	t.Parallel()
	client, result := startedTaskRun(t)
	st, err := pdstore.Open(client.dbPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close() //nolint:errcheck
	if err := st.CompleteRun(context.Background(), result.TaskID, pdstore.StatusFailed, 1, "agent failed", ""); err != nil {
		t.Fatalf("CompleteRun() error = %v", err)
	}

	info, err := client.WaitTaskRun(context.Background(), WaitTaskRunRequest{TaskID: result.TaskID, RunID: result.RunID, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitTaskRun() error = %v", err)
	}
	if info.Status != TaskRunStatusFailed || info.ErrorMessage != "agent failed" {
		t.Fatalf("info = %+v, want failed terminal state", info)
	}
}

func startedTaskRun(t *testing.T) (*Client, StartTaskRunResult) {
	t.Helper()
	client := NewClient(Config{DBPath: filepath.Join(t.TempDir(), "pd.db"), StateDir: filepath.Join(t.TempDir(), "state"), RuntimeDir: filepath.Join(t.TempDir(), "runtime")})
	client.now = func() time.Time { return time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC) }
	client.shortID = func() string { return "abcd" }
	client.launcher = &recordingLauncher{pid: 1234}
	result, err := client.StartTaskRun(context.Background(), StartTaskRunRequest{RepoPath: "/repo", Branch: "branch", WorktreePath: "/worktree", Prompt: "prompt"})
	if err != nil {
		t.Fatalf("StartTaskRun() error = %v", err)
	}
	return client, result
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
