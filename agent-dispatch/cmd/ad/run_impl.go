package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	adconfig "github.com/averycrespi/agent-tools/agent-dispatch/internal/config"
	adexec "github.com/averycrespi/agent-tools/agent-dispatch/internal/exec"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/output"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/process"
	adsandbox "github.com/averycrespi/agent-tools/agent-dispatch/internal/sandbox"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
	adworktree "github.com/averycrespi/agent-tools/agent-dispatch/internal/worktree"
	"github.com/spf13/cobra"
)

var (
	runPromptFile string
	runBranch     string
	runRepo       string
	runTemplate   string
)

type runResult struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

func init() {
	runCmd.Flags().StringVar(&runPromptFile, "prompt-file", "", "read prompt from file")
	runCmd.Flags().StringVar(&runBranch, "branch", "", "branch name to create/use")
	runCmd.Flags().StringVar(&runRepo, "repo", "", "main repository root")
	runCmd.Flags().StringVarP(&runTemplate, "template", "t", "", "template name")
	runCmd.RunE = runTask
	listCmd.RunE = listTasks
	statusCmd.RunE = showStatus
	eventsCmd.RunE = showEvents
}

func runTask(cmd *cobra.Command, args []string) error {
	cmdCtx := cmd.Context()
	prompt, source, err := resolvePrompt(args)
	if err != nil {
		return err
	}
	repo := runRepo
	if repo == "" {
		repo, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	repo, err = filepath.Abs(repo)
	if err != nil {
		return err
	}
	tmplName := runTemplate
	if tmplName == "" {
		tmplName = cfg.DefaultTemplate
	}
	tmpl, err := adconfig.FindTemplate(cfg.TemplateDirs, tmplName)
	if err != nil {
		return err
	}
	branch := runBranch
	if branch == "" {
		branch = "ad/" + slug(prompt, tmplName) + "-" + shortID()
	}
	runner := adexec.NewOSRunner()
	wt := adworktree.NewClient(runner)
	if err := wt.AddHeadless(repo, branch); err != nil {
		return err
	}
	worktreePath, err := wt.Path(repo, branch)
	if err != nil {
		return err
	}
	sb := adsandbox.NewClient(runner)
	if err := sb.Create(); err != nil {
		return err
	}
	if err := sb.CheckWorktreeVisible(worktreePath); err != nil {
		return err
	}

	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	now := time.Now()
	taskID := "ad-" + now.Format("20060102-150405") + "-" + shortID()
	runID := taskID + "-run-1"
	taskDir := adconfig.TaskDir(taskID)
	if err := os.MkdirAll(taskDir, 0o750); err != nil {
		return err
	}
	task := store.Task{ID: taskID, RepoPath: repo, RepoName: filepath.Base(repo), Branch: branch, WorktreePath: worktreePath, TemplateName: tmpl.Name, PromptSource: source, Prompt: prompt, PromptPreview: preview(prompt), Status: store.StatusQueued, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: runID, TaskID: taskID, Attempt: 1, Status: store.StatusQueued, StartedAt: now, ControlSocketPath: filepath.Join(adconfig.RuntimeDir(), "tasks", taskID+".sock"), StdoutLogPath: filepath.Join(taskDir, "stdout.log"), StderrLogPath: filepath.Join(taskDir, "stderr.log"), SupervisorLogPath: filepath.Join(taskDir, "supervisor.log"), PiEventsPath: filepath.Join(taskDir, "pi-events.jsonl")}
	if err := db.CreateTaskWithRun(cmdCtx, task, run); err != nil {
		return err
	}
	argv := adconfig.RenderPiArgv(tmpl.Agent)
	if err := process.NewLauncher(runner).StartSupervisor("--task-id", taskID, "--pi-argv", strings.Join(argv, "\x00")); err != nil {
		return err
	}
	if jsonOut {
		return output.JSON(os.Stdout, runResult{TaskID: taskID, Status: string(store.StatusQueued)})
	}
	fmt.Fprintf(os.Stdout, "Started task %s\nStatus:  ad status %s\nLogs:    ad logs -f %s\nAttach:  ad attach %s\n", taskID, taskID, taskID, taskID)
	return nil
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

func slug(prompt, tmpl string) string {
	base := preview(prompt)
	if base == "" {
		base = tmpl
	}
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
