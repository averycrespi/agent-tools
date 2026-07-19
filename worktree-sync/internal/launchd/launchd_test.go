package launchd_test

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/launchd"
)

type runner struct {
	calls       [][]string
	interactive [][]string
	outputs     [][]byte
}

func (r *runner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{dir, name}, args...))
	if len(r.outputs) == 0 {
		return nil, nil
	}
	output := r.outputs[0]
	r.outputs = r.outputs[1:]
	return output, nil
}
func (r *runner) Interactive(_ context.Context, dir, name string, args ...string) error {
	r.interactive = append(r.interactive, append([]string{dir, name}, args...))
	return nil
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
func (blockingRunner) Interactive(ctx context.Context, _ string, _ string, _ ...string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestLaunchctlCommandsHonorConfiguredTimeout(t *testing.T) {
	manager := launchd.New(blockingRunner{}, "darwin", t.TempDir(), 5*time.Millisecond)
	err := manager.Install(context.Background(), "/opt/wtsd")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestLogsShowBoundedHistoryFromBothKnownFiles(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "Library", "Logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	for _, name := range []string{"worktree-sync.log", "worktree-sync.error.log"} {
		require.NoError(t, os.WriteFile(filepath.Join(logDir, name), nil, 0o600))
	}
	r := &runner{outputs: [][]byte{[]byte("stdout entry\n"), []byte("stderr entry\n")}}
	output, err := launchd.New(r, "darwin", home, time.Second).Logs(context.Background(), 25, false)
	require.NoError(t, err)
	require.Contains(t, output, "worktree-sync.log")
	require.Contains(t, output, "stdout entry")
	require.Contains(t, output, "worktree-sync.error.log")
	require.Contains(t, output, "stderr entry")
	require.Len(t, r.calls, 2)
	for _, call := range r.calls {
		require.Contains(t, call, "25")
		require.Contains(t, call, "/usr/bin/tail")
	}
}

func TestLogsReportFilesThatHaveNotBeenCreated(t *testing.T) {
	r := &runner{}
	output, err := launchd.New(r, "darwin", t.TempDir(), time.Second).Logs(context.Background(), 100, false)
	require.NoError(t, err)
	require.Contains(t, output, "has not been created")
	require.Empty(t, r.calls)
}

func TestLogsFollowBothKnownFiles(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "Library", "Logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	for _, name := range []string{"worktree-sync.log", "worktree-sync.error.log"} {
		require.NoError(t, os.WriteFile(filepath.Join(logDir, name), nil, 0o600))
	}
	r := &runner{}
	_, err := launchd.New(r, "darwin", home, time.Second).Logs(context.Background(), 10, true)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"", "/usr/bin/tail", "-n", "10", "-F", filepath.Join(logDir, "worktree-sync.log"), filepath.Join(logDir, "worktree-sync.error.log")}}, r.interactive)
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
