package dispatcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatcher/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/control"
	pdexec "github.com/averycrespi/agent-tools/pi-dispatcher/internal/exec"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/process"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
)

type AgentOptions = pdconfig.AgentOptions

type Config struct {
	DBPath     string
	StateDir   string
	RuntimeDir string
}

type StartTaskRunRequest struct {
	RepoPath           string
	RepoName           string
	Branch             string
	WorktreePath       string
	ArtifactParentPath string
	Prompt             string
	PromptSource       string
	Agent              AgentOptions
	Env                map[string]string
	MaxDurationSeconds int64
}

type StartTaskRunResult struct {
	TaskID       string
	RunID        string
	StdoutPath   string
	StderrPath   string
	PiEventsPath string
}

type TaskRunStatus string

const (
	TaskRunStatusStarting  TaskRunStatus = "starting"
	TaskRunStatusRunning   TaskRunStatus = "running"
	TaskRunStatusSucceeded TaskRunStatus = "succeeded"
	TaskRunStatusFailed    TaskRunStatus = "failed"
	TaskRunStatusStopping  TaskRunStatus = "stopping"
	TaskRunStatusStopped   TaskRunStatus = "stopped"
	TaskRunStatusUnknown   TaskRunStatus = "unknown"
)

type TaskRunInfo struct {
	TaskID            string
	RunID             string
	SupervisorPID     int
	Status            TaskRunStatus
	ErrorMessage      string
	StdoutLogPath     string
	StderrLogPath     string
	PiEventsPath      string
	ControlSocketPath string
}

type GetTaskRunRequest struct {
	TaskID string
	RunID  string
}

type WaitTaskRunRequest struct {
	TaskID       string
	RunID        string
	PollInterval time.Duration
}

type StopTaskRunRequest struct {
	TaskID string
	RunID  string
	Force  bool
}

type supervisorLauncher interface {
	StartSupervisorWithEnv(env []string, args ...string) (int, error)
}

type Client struct {
	cfg      Config
	launcher supervisorLauncher
	now      func() time.Time
	shortID  func() string
}

func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg, launcher: process.NewLauncher(pdexec.NewOSRunner()), now: time.Now, shortID: randomShortID}
}

func (c *Client) StartTaskRun(ctx context.Context, req StartTaskRunRequest) (StartTaskRunResult, error) {
	if req.RepoPath == "" {
		return StartTaskRunResult{}, fmt.Errorf("repo path is required")
	}
	if req.Branch == "" {
		return StartTaskRunResult{}, fmt.Errorf("branch is required")
	}
	if req.WorktreePath == "" {
		return StartTaskRunResult{}, fmt.Errorf("worktree path is required")
	}
	if req.ArtifactParentPath != "" {
		info, err := os.Stat(req.ArtifactParentPath)
		if err != nil {
			return StartTaskRunResult{}, fmt.Errorf("artifact parent path %s is not accessible: %w", req.ArtifactParentPath, err)
		}
		if !info.IsDir() {
			return StartTaskRunResult{}, fmt.Errorf("artifact parent path %s is not a directory", req.ArtifactParentPath)
		}
	}
	if req.Prompt == "" {
		return StartTaskRunResult{}, fmt.Errorf("prompt is required")
	}
	if req.PromptSource == "" {
		req.PromptSource = "api"
	}
	if req.RepoName == "" {
		req.RepoName = filepath.Base(req.RepoPath)
	}
	if req.MaxDurationSeconds < 0 {
		return StartTaskRunResult{}, fmt.Errorf("max duration seconds cannot be negative")
	}

	now := c.now().UTC()
	taskID := "pd-" + now.Format("20060102-150405") + "-" + c.shortID()
	runID := taskID + "-run-1"
	taskDir := filepath.Join(c.stateDir(), "tasks", taskID)
	if err := os.MkdirAll(taskDir, 0o750); err != nil {
		return StartTaskRunResult{}, fmt.Errorf("create task dir: %w", err)
	}
	agentOptionsJSON, piArgvJSON, err := launchMetadata(req.Agent)
	if err != nil {
		return StartTaskRunResult{}, err
	}
	envNames, envValues, err := envMetadata(req.Env)
	if err != nil {
		return StartTaskRunResult{}, err
	}
	envNamesJSON, err := encodeStringList(envNames, "env names")
	if err != nil {
		return StartTaskRunResult{}, err
	}

	db, err := store.Open(c.dbPath())
	if err != nil {
		return StartTaskRunResult{}, err
	}
	defer db.Close() //nolint:errcheck
	task := store.Task{ID: taskID, RepoPath: req.RepoPath, RepoName: req.RepoName, Branch: req.Branch, WorktreePath: req.WorktreePath, PromptSource: req.PromptSource, Prompt: req.Prompt, PromptPreview: preview(req.Prompt), Status: store.StatusStarting, WorktreeCleanupPolicy: store.CleanupPolicyNever, WorktreeCreatedByPD: false, WorktreeCleanupStatus: store.CleanupStatusNotRequested, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: runID, TaskID: taskID, Attempt: 1, Status: store.StatusStarting, StartedAt: now, AgentOptionsJSON: agentOptionsJSON, PiArgvJSON: piArgvJSON, EnvVarNamesJSON: envNamesJSON, MaxDurationSeconds: req.MaxDurationSeconds, ControlSocketPath: filepath.Join(c.runtimeDir(), "tasks", taskID+".sock"), StdoutLogPath: filepath.Join(taskDir, "stdout.log"), StderrLogPath: filepath.Join(taskDir, "stderr.log"), PiEventsPath: filepath.Join(taskDir, "pi-events.jsonl")}
	if err := db.CreateTaskWithRun(ctx, task, run); err != nil {
		return StartTaskRunResult{}, err
	}
	encodedArgv, err := encodeStringList(pdconfig.RenderPiArgv(req.Agent), "Pi argv")
	if err != nil {
		return StartTaskRunResult{}, err
	}
	pid, err := c.launcher.StartSupervisorWithEnv(envValues, "--task-id", taskID, "--pi-argv", encodedArgv, "--env-names", envNamesJSON)
	if err != nil {
		_ = db.CompleteRun(ctx, taskID, store.StatusFailed, 1, err.Error(), "")
		return StartTaskRunResult{}, err
	}
	if err := db.UpdateRunSupervisorPID(ctx, taskID, pid); err != nil {
		return StartTaskRunResult{}, err
	}
	return StartTaskRunResult{TaskID: taskID, RunID: runID, StdoutPath: run.StdoutLogPath, StderrPath: run.StderrLogPath, PiEventsPath: run.PiEventsPath}, nil
}

func (c *Client) GetTaskRun(ctx context.Context, req GetTaskRunRequest) (TaskRunInfo, error) {
	if req.TaskID == "" {
		return TaskRunInfo{}, fmt.Errorf("task id is required")
	}
	db, err := store.Open(c.dbPath())
	if err != nil {
		return TaskRunInfo{}, err
	}
	defer db.Close() //nolint:errcheck
	_, err = db.GetTask(ctx, req.TaskID)
	if err != nil {
		return TaskRunInfo{}, err
	}
	run, err := db.LatestRun(ctx, req.TaskID)
	if err != nil {
		return TaskRunInfo{}, err
	}
	if req.RunID != "" && run.ID != req.RunID {
		return TaskRunInfo{}, fmt.Errorf("task %s latest run is %s, not %s", req.TaskID, run.ID, req.RunID)
	}
	return taskRunInfo(run), nil
}

func (c *Client) WaitTaskRun(ctx context.Context, req WaitTaskRunRequest) (TaskRunInfo, error) {
	if req.PollInterval <= 0 {
		req.PollInterval = time.Second
	}
	for {
		info, err := c.GetTaskRun(ctx, GetTaskRunRequest{TaskID: req.TaskID, RunID: req.RunID})
		if err != nil {
			return TaskRunInfo{}, err
		}
		if isTerminal(info.Status) {
			return info, nil
		}
		if !process.Exists(info.SupervisorPID) {
			db, openErr := store.Open(c.dbPath())
			if openErr != nil {
				return TaskRunInfo{}, openErr
			}
			if markErr := db.MarkUnknown(ctx, req.TaskID); markErr != nil {
				_ = db.Close()
				return TaskRunInfo{}, markErr
			}
			_ = db.Close()
			return c.GetTaskRun(ctx, GetTaskRunRequest{TaskID: req.TaskID, RunID: req.RunID})
		}
		timer := time.NewTimer(req.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return TaskRunInfo{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) StopTaskRun(ctx context.Context, req StopTaskRunRequest) error {
	if req.TaskID == "" {
		return fmt.Errorf("task id is required")
	}
	db, err := store.Open(c.dbPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	task, err := db.GetTask(ctx, req.TaskID)
	if err != nil {
		return err
	}
	run, err := db.LatestRun(ctx, req.TaskID)
	if err != nil {
		return err
	}
	if req.RunID != "" && run.ID != req.RunID {
		return fmt.Errorf("task %s latest run is %s, not %s", req.TaskID, run.ID, req.RunID)
	}
	if task.Status != store.StatusRunning && (!req.Force || task.Status != store.StatusStopping) {
		return fmt.Errorf("task is %s, not running", task.Status)
	}
	request := control.Request{Operation: control.OpStop, Force: req.Force}
	if _, err := control.Send(run.ControlSocketPath, request); err != nil {
		return err
	}
	return nil
}

func (c *Client) dbPath() string {
	if c.cfg.DBPath != "" {
		return c.cfg.DBPath
	}
	cfg, err := pdconfig.Load()
	if err == nil && cfg.DatabasePath != "" {
		return cfg.DBPath()
	}
	return pdconfig.DefaultDBPath()
}

func (c *Client) stateDir() string {
	if c.cfg.StateDir != "" {
		return c.cfg.StateDir
	}
	return pdconfig.StateDir()
}

func (c *Client) runtimeDir() string {
	if c.cfg.RuntimeDir != "" {
		return c.cfg.RuntimeDir
	}
	return pdconfig.RuntimeDir()
}

func taskRunInfo(run store.Run) TaskRunInfo {
	return TaskRunInfo{TaskID: run.TaskID, RunID: run.ID, SupervisorPID: run.SupervisorPID, Status: TaskRunStatus(run.Status), ErrorMessage: run.ErrorMessage, StdoutLogPath: run.StdoutLogPath, StderrLogPath: run.StderrLogPath, PiEventsPath: run.PiEventsPath, ControlSocketPath: run.ControlSocketPath}
}

func isTerminal(status TaskRunStatus) bool {
	switch status {
	case TaskRunStatusSucceeded, TaskRunStatusFailed, TaskRunStatusStopped, TaskRunStatusUnknown:
		return true
	default:
		return false
	}
}

func launchMetadata(agent AgentOptions) (string, string, error) {
	agentOptions, err := json.Marshal(agent)
	if err != nil {
		return "", "", err
	}
	piArgv, err := json.Marshal(pdconfig.RenderPiArgv(agent))
	if err != nil {
		return "", "", err
	}
	return string(agentOptions), string(piArgv), nil
}

func envMetadata(env map[string]string) ([]string, []string, error) {
	names := make([]string, 0, len(env))
	values := make([]string, 0, len(env))
	for name, value := range env {
		if !validEnvName(name) {
			return nil, nil, fmt.Errorf("invalid env var name: %s", name)
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, nil, fmt.Errorf("env var %s contains NUL byte", name)
		}
		names = append(names, name)
		values = append(values, name+"="+value)
	}
	return names, values, nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		valid := r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9'
		if !valid {
			return false
		}
	}
	return true
}

func encodeStringList(values []string, label string) (string, error) {
	for _, value := range values {
		if strings.ContainsRune(value, '\x00') {
			return "", fmt.Errorf("%s contains NUL byte", label)
		}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func preview(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

func randomShortID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	}
	return hex.EncodeToString(b[:])
}
