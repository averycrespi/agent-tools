//go:build darwin || linux

package runtimes

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func fixtureProcessGroup() int {
	group, _ := unix.Getpgid(0)
	return group
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
