package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

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

	require.Contains(t, out, "Raw Pi events: /events/pi-events.jsonl")
	require.Contains(t, out, "Provider: openai")
	require.Contains(t, out, "Model:    gpt-5")
	require.Contains(t, out, "Tools:    bash")
	require.Contains(t, out, "Env:      OPENAI_API_KEY, EMPTY")
	require.NotContains(t, out, "Pi argv:")
	require.NotContains(t, out, "--system-prompt")
	require.NotContains(t, out, "Events:  /events/pi-events.jsonl")
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
