package inspect

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatcher/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
)

const (
	DefaultLogLimit       = 64 * 1024
	MaxLogLimit           = 1024 * 1024
	maxPiEventsScanBytes  = 512 * 1024
	maxResponsePreviewLen = 4000
)

type TaskSummary struct {
	ID                         string      `json:"id"`
	RepoPath                   string      `json:"repo_path"`
	RepoName                   string      `json:"repo_name"`
	Branch                     string      `json:"branch"`
	WorktreePath               string      `json:"worktree_path"`
	WorktreeCleanupPolicy      string      `json:"worktree_cleanup_policy"`
	WorktreeCreatedByPD        bool        `json:"worktree_created_by_pd"`
	WorktreeCleanupStatus      string      `json:"worktree_cleanup_status"`
	WorktreeCleanupError       string      `json:"worktree_cleanup_error,omitempty"`
	WorktreeCleanupAttemptedAt *time.Time  `json:"worktree_cleanup_attempted_at,omitempty"`
	WorktreeRemovedAt          *time.Time  `json:"worktree_removed_at,omitempty"`
	PromptSource               string      `json:"prompt_source"`
	Prompt                     string      `json:"prompt,omitempty"`
	PromptPreview              string      `json:"prompt_preview"`
	PromptTruncated            bool        `json:"prompt_truncated"`
	Status                     string      `json:"status"`
	CreatedAt                  time.Time   `json:"created_at"`
	UpdatedAt                  time.Time   `json:"updated_at"`
	LatestRun                  *RunSummary `json:"latest_run,omitempty"`
}

type TaskDetail struct {
	Task              TaskSummary `json:"task"`
	LatestRun         *RunSummary `json:"latest_run,omitempty"`
	ResponsePreview   string      `json:"response_preview,omitempty"`
	ResponseTruncated bool        `json:"response_truncated"`
}

type RunSummary struct {
	ID                 string              `json:"id"`
	TaskID             string              `json:"task_id"`
	Attempt            int                 `json:"attempt"`
	SupervisorPID      int                 `json:"supervisor_pid"`
	PiSessionFile      string              `json:"pi_session_file"`
	Status             string              `json:"status"`
	StartedAt          time.Time           `json:"started_at"`
	EndedAt            *time.Time          `json:"ended_at,omitempty"`
	ExitCode           *int64              `json:"exit_code,omitempty"`
	ErrorMessage       string              `json:"error_message"`
	AgentOptions       AgentOptionsSummary `json:"agent_options"`
	EnvVarNames        []string            `json:"env_var_names,omitempty"`
	MaxDurationSeconds int64               `json:"max_duration_seconds,omitempty"`
	ControlSocketPath  string              `json:"control_socket_path"`
	StdoutLogPath      string              `json:"stdout_log_path"`
	StderrLogPath      string              `json:"stderr_log_path"`
	PiEventsPath       string              `json:"pi_events_path"`
}

type AgentOptionsSummary struct {
	Provider                  string   `json:"provider,omitempty"`
	Model                     string   `json:"model,omitempty"`
	Thinking                  string   `json:"thinking,omitempty"`
	Tools                     []string `json:"tools,omitempty"`
	DisableBuiltinTools       bool     `json:"disable_builtin_tools,omitempty"`
	DisableAllTools           bool     `json:"disable_all_tools,omitempty"`
	Extensions                []string `json:"extensions,omitempty"`
	DisableExtensionDiscovery bool     `json:"disable_extension_discovery,omitempty"`
	Skills                    []string `json:"skills,omitempty"`
	DisableSkillDiscovery     bool     `json:"disable_skill_discovery,omitempty"`
	DisableContextFiles       bool     `json:"disable_context_files,omitempty"`
	SessionDir                string   `json:"session_dir,omitempty"`
}

type LogWindow struct {
	Stream     string `json:"stream"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	Content    string `json:"content"`
}

func TaskSummaryView(summary store.TaskSummary) TaskSummary {
	view := TaskSummaryFromTask(summary.Task)
	if summary.LatestRun.Valid {
		view.LatestRun = RunSummaryView(summary.LatestRun.Run)
	}
	return view
}

func TaskSummaryFromTask(task store.Task) TaskSummary {
	view := TaskSummary{ID: task.ID, RepoPath: task.RepoPath, RepoName: task.RepoName, Branch: task.Branch, WorktreePath: task.WorktreePath, WorktreeCleanupPolicy: string(task.WorktreeCleanupPolicy), WorktreeCreatedByPD: task.WorktreeCreatedByPD, WorktreeCleanupStatus: string(task.WorktreeCleanupStatus), WorktreeCleanupError: task.WorktreeCleanupError, PromptSource: task.PromptSource, Prompt: task.Prompt, PromptPreview: task.PromptPreview, PromptTruncated: promptTruncated(task.Prompt, task.PromptPreview), Status: string(task.Status), CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if task.WorktreeCleanupAttemptedAt.Valid {
		attemptedAt := task.WorktreeCleanupAttemptedAt.Time
		view.WorktreeCleanupAttemptedAt = &attemptedAt
	}
	if task.WorktreeRemovedAt.Valid {
		removedAt := task.WorktreeRemovedAt.Time
		view.WorktreeRemovedAt = &removedAt
	}
	return view
}

func RunSummaryView(run store.Run) *RunSummary {
	view := &RunSummary{ID: run.ID, TaskID: run.TaskID, Attempt: run.Attempt, SupervisorPID: run.SupervisorPID, PiSessionFile: run.PiSessionFile, Status: string(run.Status), StartedAt: run.StartedAt, ErrorMessage: run.ErrorMessage, AgentOptions: decodeAgentOptions(run.AgentOptionsJSON), EnvVarNames: decodeEnvVarNames(run.EnvVarNamesJSON), MaxDurationSeconds: run.MaxDurationSeconds, ControlSocketPath: run.ControlSocketPath, StdoutLogPath: run.StdoutLogPath, StderrLogPath: run.StderrLogPath, PiEventsPath: run.PiEventsPath}
	if run.EndedAt.Valid {
		ended := run.EndedAt.Time
		view.EndedAt = &ended
	}
	if run.ExitCode.Valid {
		exitCode := run.ExitCode.Int64
		view.ExitCode = &exitCode
	}
	return view
}

func ResponsePreviewFromPiEvents(piEventsPath string) (string, bool) {
	piEvents, err := readTail(piEventsPath, maxPiEventsScanBytes)
	if err != nil {
		return "", false
	}
	response, ok := lastAssistantResponseFromPiEvents(piEvents)
	if !ok {
		return "", false
	}
	if len(response) <= maxResponsePreviewLen {
		return response, false
	}
	return response[:maxResponsePreviewLen], true
}

func ReadLogWindow(path string, stream string, offset int64, limit int) (LogWindow, error) {
	if limit > MaxLogLimit {
		return LogWindow{}, fmt.Errorf("limit must be less than or equal to %d", MaxLogLimit)
	}
	file, err := os.Open(path) //nolint:gosec // log path comes from pd-owned task state.
	if err != nil {
		return LogWindow{}, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return LogWindow{}, err
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	buf := make([]byte, limit)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		return LogWindow{}, err
	}
	return LogWindow{Stream: stream, Offset: offset, NextOffset: offset + int64(n), Size: info.Size(), Content: string(buf[:n])}, nil
}

func promptTruncated(prompt, preview string) bool {
	normalized := strings.TrimSpace(strings.Join(strings.Fields(prompt), " "))
	return len(normalized) > len(preview)
}

func decodeEnvVarNames(data string) []string {
	if data == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(data), &names); err != nil {
		return nil
	}
	return names
}

func decodeAgentOptions(data string) AgentOptionsSummary {
	if data == "" {
		return AgentOptionsSummary{}
	}
	var agent pdconfig.AgentOptions
	if err := json.Unmarshal([]byte(data), &agent); err != nil {
		return AgentOptionsSummary{}
	}
	return AgentOptionsSummary{Provider: agent.Provider, Model: agent.Model, Thinking: agent.Thinking, Tools: agent.Tools, DisableBuiltinTools: agent.DisableBuiltinTools, DisableAllTools: agent.DisableAllTools, Extensions: agent.Extensions, DisableExtensionDiscovery: agent.DisableExtensionDiscovery, Skills: agent.Skills, DisableSkillDiscovery: agent.DisableSkillDiscovery, DisableContextFiles: agent.DisableContextFiles, SessionDir: agent.SessionDir}
}

func readTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // path comes from pd-owned task state.
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > limit {
		offset = size - limit
		size = limit
	}
	buf := make([]byte, size)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		return nil, err
	}
	return buf[:n], nil
}

func lastAssistantResponseFromPiEvents(data []byte) (string, bool) {
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		text, ok := assistantResponseFromPiEvent([]byte(lines[i]))
		if ok {
			return text, true
		}
	}
	return "", false
}

func assistantResponseFromPiEvent(data []byte) (string, bool) {
	var event struct {
		Message  messagePayload   `json:"message"`
		Messages []messagePayload `json:"messages"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return "", false
	}
	if text, ok := assistantResponseFromMessage(event.Message); ok {
		return text, true
	}
	for i := len(event.Messages) - 1; i >= 0; i-- {
		if text, ok := assistantResponseFromMessage(event.Messages[i]); ok {
			return text, true
		}
	}
	return "", false
}

func assistantResponseFromMessage(message messagePayload) (string, bool) {
	if message.Role != "assistant" {
		return "", false
	}
	text := strings.TrimSpace(messageText(message.Content))
	return text, text != ""
}

type messagePayload struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func messageText(content json.RawMessage) string {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			out = append(out, part.Text)
		}
	}
	return strings.Join(out, "\n")
}
