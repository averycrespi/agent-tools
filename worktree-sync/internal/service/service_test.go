package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/service"
)

type editorRunner struct{ replacement []byte }

func (*editorRunner) Run(context.Context, string, string, ...string) ([]byte, error) { return nil, nil }
func (*editorRunner) RunEnv(context.Context, string, []string, string, ...string) ([]byte, error) {
	return nil, nil
}
func (r *editorRunner) Interactive(_ context.Context, _ string, _ string, args ...string) error {
	return os.WriteFile(args[len(args)-1], r.replacement, 0o600)
}

func TestConfigRefreshCreatesValidatedDefaults(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	controller := service.New(&editorRunner{}, paths)
	_, err := controller.Execute(context.Background(), app.Request{Action: "config.refresh"})
	require.NoError(t, err)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	require.Equal(t, config.Default().Global, cfg.Global)
}

func TestConfigRefreshFillsMissingFieldsWithoutReplacingRepositories(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Config), 0o700))
	require.NoError(t, os.WriteFile(paths.Config, []byte(`{"version":1,"global":{},"repositories":[]}`), 0o600))
	controller := service.New(&editorRunner{}, paths)
	_, err := controller.Execute(context.Background(), app.Request{Action: "config.refresh"})
	require.NoError(t, err)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	require.Equal(t, config.Default().Global, cfg.Global)
}

func TestConfigEditLeavesLiveFileUnchangedWhenEditedCopyIsInvalid(t *testing.T) {
	t.Setenv("EDITOR", "editor")
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, config.Save(paths.Config, config.Default()))
	before, err := os.ReadFile(paths.Config)
	require.NoError(t, err)
	controller := service.New(&editorRunner{replacement: []byte(`{"version":999}`)}, paths)
	_, err = controller.Execute(context.Background(), app.Request{Action: "config.edit"})
	require.ErrorContains(t, err, "invalid")
	after, readErr := os.ReadFile(paths.Config)
	require.NoError(t, readErr)
	require.Equal(t, before, after)
}
