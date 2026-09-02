//go:build darwin || linux

package runtimes

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func fixtureProcessGroup() int {
	group, _ := unix.Getpgid(0)
	return group
}

func TestStdioHardCrashOwner(t *testing.T) {
	if os.Getenv("MCP_GATEWAY_HARD_CRASH_OWNER") != "1" {
		return
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	runtime, err := NewStdioSupervisor(nil).Start(context.Background(), fixtureDefinition(executable, "ignore-term"))
	require.NoError(t, err)
	<-runtime.Frames()
	require.NoError(t, json.NewEncoder(os.Stdout).Encode(runtime.command.Process.Pid))
	select {}
}

func TestHardCrashRestartDoesNotActOnOrClaimOrphanedPID(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	owner := exec.Command(executable, "-test.run=TestStdioHardCrashOwner") //nolint:gosec // reexecutes the controlled test binary directly.
	owner.Env = []string{"MCP_GATEWAY_HARD_CRASH_OWNER=1"}
	stdout, err := owner.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, owner.Start())
	var childPID int
	require.NoError(t, json.NewDecoder(stdout).Decode(&childPID))
	defer func() { _ = syscall.Kill(-childPID, syscall.SIGKILL) }()
	require.NoError(t, owner.Process.Kill())
	assert.Error(t, owner.Wait())
	assert.NoError(t, syscall.Kill(childPID, 0))
	assert.Zero(t, NewStdioSupervisor(nil).Status().InUse)
}

func TestStdioSupervisorCreatesNewProcessGroup(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	runtime, err := NewStdioSupervisor(nil).Start(context.Background(), StdioDefinition{
		RuntimeID: "runtime-process-group", Executable: executable,
		Arguments: []string{"-test.run=TestStdioFixtureProcess", "--", "inspect"}, WorkingDirectory: "/",
		Environment: map[string]string{stdioFixtureMarker: "1"}, SecretEnvironment: map[string]string{}, Secrets: map[string]string{},
	})
	require.NoError(t, err)
	var inspection fixtureInspection
	require.NoError(t, json.Unmarshal(receiveFrame(t, runtime.Frames()), &inspection))
	assert.Equal(t, inspection.PID, inspection.ProcessGroup)
	_ = receiveExit(t, runtime.Done())
}
