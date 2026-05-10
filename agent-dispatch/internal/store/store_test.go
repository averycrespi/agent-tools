package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenCreatesSchemaAndInsertTaskRun(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ad.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "ad-test", RepoPath: "/repo", RepoName: "repo", Branch: "ad/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusQueued, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusQueued, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", SupervisorLogPath: "/supervisor", PiEventsPath: "/events"}

	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
}
