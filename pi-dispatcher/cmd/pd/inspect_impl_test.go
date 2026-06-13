package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatcher/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestViewRunIncludesMaxDuration(t *testing.T) {
	view := viewRun(store.Run{ID: "run-test", Status: store.StatusRunning, Attempt: 1, MaxDurationSeconds: 7200})

	require.Equal(t, int64(7200), view.MaxDurationSeconds)
}

func TestShowStatusLabelsPiEventsAsRawArtifact(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })

	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now()
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: store.StatusSucceeded, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: store.StatusSucceeded, StartedAt: now, AgentOptionsJSON: `{"provider":"openai","model":"gpt-5","tools":["bash"],"system_prompt":"secret"}`, PiArgvJSON: `["pi","--mode","rpc","--provider","openai","--model","gpt-5","--tools","bash","--system-prompt","secret"]`, EnvVarNamesJSON: `["OPENAI_API_KEY","EMPTY"]`, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events/pi-events.jsonl"}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	require.NoError(t, db.Close())

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() {
		require.NoError(t, showStatus(cmd, []string{task.ID}))
	})

	require.Regexp(t, `Raw Pi events:\s+/events/pi-events\.jsonl`, out)
	require.Regexp(t, `Provider:\s+openai`, out)
	require.Regexp(t, `Model:\s+gpt-5`, out)
	require.Regexp(t, `Tools:\s+bash`, out)
	require.Regexp(t, `Env:\s+OPENAI_API_KEY, EMPTY`, out)
	require.NotContains(t, out, "Pi argv:")
	require.NotContains(t, out, "--system-prompt")
	require.NotRegexp(t, `(^|\s)Events:\s+/events/pi-events\.jsonl`, out)
	require.NotContains(t, out, "secret")
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writePipe
	defer func() { os.Stdout = old }()

	fn()
	require.NoError(t, writePipe.Close())
	out, err := io.ReadAll(readPipe)
	require.NoError(t, err)
	require.NoError(t, readPipe.Close())
	return string(out)
}
