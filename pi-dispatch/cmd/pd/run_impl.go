package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	pdexec "github.com/averycrespi/agent-tools/pi-dispatch/internal/exec"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/gitmeta"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/output"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/process"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	sbsandbox "github.com/averycrespi/agent-tools/sandbox-manager/pkg/sandbox"
	wtworktree "github.com/averycrespi/agent-tools/worktree-manager/pkg/worktree"
	"github.com/spf13/cobra"
)

var (
	runPromptFile     string
	runBranch         string
	runRepo           string
	runAgentOverrides pdconfig.AgentOptions
)

type runResult struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

type worktreeClient interface {
	AddHeadless(repoRoot, branch string) (string, error)
	Remove(repoRoot, branch string) error
}

type sandboxClient interface {
	Create() error
	Exec(workdir string, args ...string) ([]byte, error)
}

var (
	newWorktreeClient = func() (worktreeClient, error) { return wtworktree.New() }
	newSandboxClient  = func() (sandboxClient, error) { return sbsandbox.New() }
)

func init() {
	runCmd.Flags().StringVar(&runPromptFile, "prompt-file", "", "read prompt from file")
	runCmd.Flags().StringVar(&runBranch, "branch", "", "branch name to create/use")
	runCmd.Flags().StringVar(&runRepo, "repo", "", "main repository root")
	runCmd.Flags().StringVar(&runAgentOverrides.Provider, "provider", "", "Pi provider override")
	runCmd.Flags().StringVar(&runAgentOverrides.Model, "model", "", "Pi model override")
	runCmd.Flags().StringVar(&runAgentOverrides.Thinking, "thinking", "", "Pi thinking level override")
	runCmd.Flags().StringArrayVar(&runAgentOverrides.Tools, "tools", nil, "Pi tool allowlist entry")
	runCmd.Flags().BoolVar(&runAgentOverrides.DisableBuiltinTools, "no-builtin-tools", false, "disable built-in Pi tools")
	runCmd.Flags().BoolVar(&runAgentOverrides.DisableAllTools, "no-tools", false, "disable all Pi tools")
	runCmd.Flags().StringArrayVarP(&runAgentOverrides.Extensions, "extension", "e", nil, "Pi extension path or name")
	runCmd.Flags().BoolVar(&runAgentOverrides.DisableExtensionDiscovery, "no-extensions", false, "disable Pi extension discovery")
	runCmd.Flags().StringArrayVar(&runAgentOverrides.Skills, "skill", nil, "Pi skill name")
	runCmd.Flags().BoolVar(&runAgentOverrides.DisableSkillDiscovery, "no-skills", false, "disable Pi skill discovery")
	runCmd.Flags().BoolVar(&runAgentOverrides.DisableContextFiles, "no-context-files", false, "disable Pi context files")
	runCmd.Flags().StringVar(&runAgentOverrides.SystemPrompt, "system-prompt", "", "Pi system prompt override")
	runCmd.Flags().StringVar(&runAgentOverrides.AppendSystemPrompt, "append-system-prompt", "", "text to append to Pi system prompt")
	runCmd.Flags().StringVar(&runAgentOverrides.SessionDir, "session-dir", "", "Pi session directory")
	runCmd.RunE = runTask
	listCmd.RunE = listTasks
	statusCmd.RunE = showStatus
}

func runTask(cmd *cobra.Command, args []string) error {
	cmdCtx := cmd.Context()
	prompt, source, err := resolvePrompt(args)
	if err != nil {
		return err
	}
	runner := pdexec.NewOSRunner()
	repo, err := resolveRunRepoInfo(runner, runRepo)
	if err != nil {
		return err
	}
	branch := runBranch
	if branch == "" {
		branch = "pd/" + slug(prompt) + "-" + shortID()
	}
	wt, err := newWorktreeClient()
	if err != nil {
		return err
	}
	worktreePath, err := wt.AddHeadless(repo.Root, branch)
	if err != nil {
		return err
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	now := time.Now()
	taskID := "pd-" + now.Format("20060102-150405") + "-" + shortID()
	runID := taskID + "-run-1"
	taskDir := pdconfig.TaskDir(taskID)
	if err := os.MkdirAll(taskDir, 0o750); err != nil {
		return err
	}
	task := store.Task{ID: taskID, RepoPath: repo.Root, RepoName: repo.Name, Branch: branch, WorktreePath: worktreePath, PromptSource: source, Prompt: prompt, PromptPreview: preview(prompt), Status: store.StatusStarting, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: runID, TaskID: taskID, Attempt: 1, Status: store.StatusStarting, StartedAt: now, ControlSocketPath: filepath.Join(pdconfig.RuntimeDir(), "tasks", taskID+".sock"), StdoutLogPath: filepath.Join(taskDir, "stdout.log"), StderrLogPath: filepath.Join(taskDir, "stderr.log"), PiEventsPath: filepath.Join(taskDir, "pi-events.jsonl")}
	if err := db.CreateTaskWithRun(cmdCtx, task, run); err != nil {
		return err
	}
	sb, err := newSandboxClient()
	if err != nil {
		return failRunLaunch(cmdCtx, db, taskID, err)
	}
	if err := sb.Create(); err != nil {
		return failRunLaunch(cmdCtx, db, taskID, err)
	}
	if err := checkWorktreeVisible(sb, worktreePath); err != nil {
		return failRunLaunch(cmdCtx, db, taskID, err)
	}
	argv := pdconfig.RenderPiArgv(applyRunOverrides(pdconfig.AgentOptions{}))
	encodedArgv, err := encodePiArgv(argv)
	if err != nil {
		return failRunLaunch(cmdCtx, db, taskID, err)
	}
	pid, err := process.NewLauncher(runner).StartSupervisor("--task-id", taskID, "--pi-argv", encodedArgv)
	if err != nil {
		return failRunLaunch(cmdCtx, db, taskID, err)
	}
	if err := db.UpdateRunSupervisorPID(cmdCtx, taskID, pid); err != nil {
		return failRunLaunch(cmdCtx, db, taskID, err)
	}
	if jsonOut {
		return output.JSON(os.Stdout, runResult{TaskID: taskID, Status: string(store.StatusStarting)})
	}
	_, err = fmt.Fprintf(os.Stdout, "Started task %s\nStatus:  pd status %s\nLogs:    pd logs -f %s\nAttach:  pd attach %s\n", taskID, taskID, taskID, taskID)
	return err
}

func failRunLaunch(ctx context.Context, db *store.Store, taskID string, err error) error {
	if completeErr := db.CompleteRun(ctx, taskID, store.StatusFailed, 1, err.Error(), ""); completeErr != nil {
		return completeErr
	}
	return err
}

func resolveRunRepoInfo(runner pdexec.Runner, repoArg string) (gitmeta.Info, error) {
	repo := repoArg
	if repo == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return gitmeta.Info{}, err
		}
		repo = cwd
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return gitmeta.Info{}, err
	}
	return gitmeta.NewClient(runner).Info(repo)
}

func checkWorktreeVisible(sb interface {
	Exec(workdir string, args ...string) ([]byte, error)
}, worktreePath string) error {
	if _, err := sb.Exec("/", "test", "-d", worktreePath); err != nil {
		return fmt.Errorf("worktree is not visible inside the sandbox: %s\n\nAdd the worktree base directory as a writable sb mount, then recreate the Lima VM so mount changes apply", worktreePath)
	}
	return nil
}

func applyRunOverrides(agent pdconfig.AgentOptions) pdconfig.AgentOptions {
	return pdconfig.ApplyAgentOverrides(agent, runAgentOverrides)
}

func resolvePrompt(args []string) (string, string, error) {
	sources := 0
	if len(args) > 0 {
		sources++
	}
	if runPromptFile != "" {
		sources++
	}
	stdin := len(args) == 1 && args[0] == "-"
	if stdin && runPromptFile != "" {
		return "", "", fmt.Errorf("exactly one prompt source is allowed")
	}
	if sources != 1 {
		return "", "", fmt.Errorf("exactly one prompt source is allowed")
	}
	if stdin {
		data, err := os.ReadFile("/dev/stdin") //nolint:gosec
		return string(data), "stdin", err
	}
	if runPromptFile != "" {
		data, err := os.ReadFile(runPromptFile) //nolint:gosec
		return string(data), "file", err
	}
	return strings.Join(args, " "), "arg", nil
}

func shortID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	}
	return hex.EncodeToString(b[:])
}

func slug(prompt string) string {
	base := preview(prompt)
	if base == "" {
		base = "task"
	}
	base = strings.ToLower(base)
	var out strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
		} else if out.Len() > 0 && out.String()[out.Len()-1] != '-' {
			out.WriteByte('-')
		}
		if out.Len() >= 32 {
			break
		}
	}
	result := strings.Trim(out.String(), "-")
	if result == "" {
		return "task"
	}
	return result
}

func preview(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > 80 {
		return s[:80]
	}
	return s
}
