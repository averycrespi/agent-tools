package launchd_test

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/launchd"
)

type runner struct{ calls [][]string }

func (r *runner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{dir, name}, args...))
	return nil, nil
}

func TestRenderContainsPortableAbsoluteDaemonAndRequiredKeys(t *testing.T) {
	data, err := launchd.Render("/opt/tools/wtsd", "/Users/test")
	require.NoError(t, err)
	var value any
	require.NoError(t, xml.Unmarshal(data, &value))
	text := string(data)
	for _, expected := range []string{"dev.agent-tools.worktree-sync", "/opt/tools/wtsd", "PATH", "RunAtLoad", "KeepAlive", "StandardOutPath", "StandardErrorPath"} {
		require.Contains(t, text, expected)
	}
}

func TestManagerRejectsNonDarwinWithoutRunningCommands(t *testing.T) {
	r := &runner{}
	manager := launchd.New(r, "linux", "/home/test", time.Second)
	err := manager.Install(context.Background(), "/usr/bin/wtsd")
	require.ErrorContains(t, err, "macOS")
	require.Empty(t, r.calls)
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLaunchctlCommandsHonorConfiguredTimeout(t *testing.T) {
	manager := launchd.New(blockingRunner{}, "darwin", t.TempDir(), 5*time.Millisecond)
	err := manager.Install(context.Background(), "/opt/wtsd")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestInstallAndUninstallAffectOnlyOwnLabel(t *testing.T) {
	home := t.TempDir()
	r := &runner{}
	manager := launchd.New(r, "darwin", home, time.Second)
	require.NoError(t, manager.Install(context.Background(), "/opt/wtsd"))
	require.NoError(t, manager.Install(context.Background(), "/opt/wtsd"))
	require.NoError(t, manager.Start(context.Background()))
	require.NoError(t, manager.Stop(context.Background()))
	_, err := manager.Status(context.Background())
	require.NoError(t, err)
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.agent-tools.worktree-sync.plist")
	require.FileExists(t, plist)
	require.NoError(t, manager.Uninstall(context.Background()))
	require.NoFileExists(t, plist)
	for _, call := range r.calls {
		require.Contains(t, strings.Join(call, " "), "dev.agent-tools.worktree-sync")
	}
}
