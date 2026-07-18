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

func TestInstallAndUninstallAffectOnlyOwnLabel(t *testing.T) {
	home := t.TempDir()
	r := &runner{}
	manager := launchd.New(r, "darwin", home, time.Second)
	require.NoError(t, manager.Install(context.Background(), "/opt/wtsd"))
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.agent-tools.worktree-sync.plist")
	require.FileExists(t, plist)
	require.NoError(t, manager.Uninstall(context.Background()))
	require.NoFileExists(t, plist)
	for _, call := range r.calls {
		require.Contains(t, strings.Join(call, " "), "dev.agent-tools.worktree-sync")
	}
}
