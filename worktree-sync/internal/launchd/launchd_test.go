package launchd_test

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/launchd"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
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

func testEnvironment(root string) launchd.Environment {
	return launchd.Environment{ConfigHome: filepath.Join(root, "config"), DataHome: filepath.Join(root, "data"), StateHome: filepath.Join(root, "state")}
}

func TestRenderContainsPortableAbsoluteDaemonAndXDGEnvironment(t *testing.T) {
	environment := launchd.Environment{ConfigHome: "/Users/test/cfg&one", DataHome: "/Users/test/data", StateHome: "/Users/test/state"}
	data, err := launchd.Render("/opt/tools/wtsd", "/Users/test", environment)
	require.NoError(t, err)
	var value any
	require.NoError(t, xml.Unmarshal(data, &value))
	text := string(data)
	for _, expected := range []string{"dev.agent-tools.worktree-sync", "/opt/tools/wtsd", "PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "RunAtLoad", "KeepAlive", "StandardOutPath", "StandardErrorPath", "cfg&amp;one"} {
		require.Contains(t, text, expected)
	}
}

func TestManagerRejectsNonDarwinWithoutRunningCommands(t *testing.T) {
	r := &runner{}
	manager := launchd.New(r, "linux", "/home/test", time.Second)
	_, err := manager.Install(context.Background(), "/usr/bin/wtsd", testEnvironment("/tmp"))
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
	_, err := manager.Install(context.Background(), "/opt/wtsd", testEnvironment(t.TempDir()))
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

type lifecycleRunner struct {
	mu                sync.Mutex
	calls             [][]string
	loaded            bool
	failBootstrapOnce bool
	failBootoutOnce   bool
	inspectErr        error
}

func (r *lifecycleRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{dir, name}, args...))
	if name != "launchctl" || len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "print":
		if r.inspectErr != nil {
			return []byte("launchctl unavailable"), r.inspectErr
		}
		if !r.loaded {
			return []byte("Could not find service"), errors.New("exit status 113")
		}
		return []byte("state = running"), nil
	case "bootstrap":
		if r.failBootstrapOnce {
			r.failBootstrapOnce = false
			return []byte("bootstrap failed"), errors.New("exit status 5")
		}
		r.loaded = true
	case "bootout":
		if r.failBootoutOnce {
			r.failBootoutOnce = false
			return []byte("bootout failed"), errors.New("exit status 5")
		}
		r.loaded = false
	}
	return nil, nil
}

func (r *lifecycleRunner) Interactive(context.Context, string, string, ...string) error { return nil }

func TestLaunchAgentLifecycleIsTruthfulAndIdempotent(t *testing.T) {
	home := t.TempDir()
	r := &lifecycleRunner{}
	manager := launchd.New(r, "darwin", home, time.Second)
	environment := testEnvironment(t.TempDir())

	status, err := manager.Status(context.Background())
	require.NoError(t, err)
	require.Contains(t, status, "not installed")
	output, err := manager.Stop(context.Background())
	require.NoError(t, err)
	require.Contains(t, output, "not installed")
	_, err = manager.Start(context.Background())
	require.ErrorContains(t, err, "daemon install")

	output, err = manager.Install(context.Background(), "/opt/wtsd", environment)
	require.NoError(t, err)
	require.Contains(t, output, "installed")
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.agent-tools.worktree-sync.plist")
	info, err := os.Stat(plist)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.True(t, r.loaded)

	output, err = manager.Start(context.Background())
	require.NoError(t, err)
	require.Contains(t, output, "already running")
	output, err = manager.Stop(context.Background())
	require.NoError(t, err)
	require.Contains(t, output, "stopped")
	require.False(t, r.loaded)
	require.FileExists(t, plist)
	output, err = manager.Stop(context.Background())
	require.NoError(t, err)
	require.Contains(t, output, "already stopped")
	status, err = manager.Status(context.Background())
	require.NoError(t, err)
	require.Contains(t, status, "stopped")

	output, err = manager.Restart(context.Background())
	require.NoError(t, err)
	require.Contains(t, output, "restarted")
	require.True(t, r.loaded)
	output, err = manager.Uninstall(context.Background())
	require.NoError(t, err)
	require.Contains(t, output, "uninstalled")
	require.False(t, r.loaded)
	require.NoFileExists(t, plist)

	for _, call := range r.calls {
		require.Contains(t, strings.Join(call, " "), "dev.agent-tools.worktree-sync")
	}
}

func TestInstallRestoresPreviousPlistWhenBootoutFails(t *testing.T) {
	home := t.TempDir()
	r := &lifecycleRunner{}
	manager := launchd.New(r, "darwin", home, time.Second)
	_, err := manager.Install(context.Background(), "/opt/old-wtsd", testEnvironment(filepath.Join(t.TempDir(), "old")))
	require.NoError(t, err)

	r.failBootoutOnce = true
	output, err := manager.Install(context.Background(), "/opt/new-wtsd", testEnvironment(filepath.Join(t.TempDir(), "new")))
	require.Error(t, err)
	require.Contains(t, output, "previous plist restored")
	data, readErr := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.agent-tools.worktree-sync.plist"))
	require.NoError(t, readErr)
	require.Contains(t, string(data), "/opt/old-wtsd")
	require.NotContains(t, string(data), "/opt/new-wtsd")
}

func TestInstallRestoresPreviousAgentWhenReplacementBootstrapFails(t *testing.T) {
	home := t.TempDir()
	r := &lifecycleRunner{}
	manager := launchd.New(r, "darwin", home, time.Second)
	oldEnvironment := testEnvironment(filepath.Join(t.TempDir(), "old"))
	_, err := manager.Install(context.Background(), "/opt/old-wtsd", oldEnvironment)
	require.NoError(t, err)

	r.failBootstrapOnce = true
	output, err := manager.Install(context.Background(), "/opt/new-wtsd", testEnvironment(filepath.Join(t.TempDir(), "new")))
	require.Error(t, err)
	require.Contains(t, output, "previous LaunchAgent restored")
	require.True(t, r.loaded)
	data, readErr := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.agent-tools.worktree-sync.plist"))
	require.NoError(t, readErr)
	require.Contains(t, string(data), "/opt/old-wtsd")
	require.NotContains(t, string(data), "/opt/new-wtsd")
}

func TestLifecycleUsesFixedLockIndependentOfXDGEnvironment(t *testing.T) {
	home := t.TempDir()
	lockPath := filepath.Join(home, "Library", "Application Support", "worktree-sync", "launchd.lock")
	lock, err := state.Acquire(context.Background(), lockPath)
	require.NoError(t, err)
	r := &lifecycleRunner{}
	manager := launchd.New(r, "darwin", home, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = manager.Install(ctx, "/opt/wtsd", testEnvironment(filepath.Join(t.TempDir(), "first-xdg")))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, r.calls)
	require.NoError(t, lock.Unlock())

	_, err = manager.Install(context.Background(), "/opt/wtsd", testEnvironment(filepath.Join(t.TempDir(), "second-xdg")))
	require.NoError(t, err)
}

func TestInstallAbortsWhenLaunchctlStateIsUnavailable(t *testing.T) {
	home := t.TempDir()
	r := &lifecycleRunner{inspectErr: errors.New("permission denied")}
	manager := launchd.New(r, "darwin", home, time.Second)
	_, err := manager.Install(context.Background(), "/opt/wtsd", testEnvironment(t.TempDir()))
	require.ErrorContains(t, err, "observing LaunchAgent")
	require.NoFileExists(t, filepath.Join(home, "Library", "LaunchAgents", "dev.agent-tools.worktree-sync.plist"))
}
