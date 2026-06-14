package process

import (
	"os"
	"testing"

	pdexec "github.com/averycrespi/agent-tools/pi-dispatcher/internal/exec"
	"github.com/stretchr/testify/require"
)

func TestStartSupervisorReturnsDetachedPID(t *testing.T) {
	runner := &fakeRunner{pid: 4242}
	pid, err := NewLauncher(runner).StartSupervisor("--task-id", "pd-test")
	require.NoError(t, err)
	require.Equal(t, 4242, pid)
	exe, err := os.Executable()
	require.NoError(t, err)
	require.Equal(t, exe, runner.name)
	require.Equal(t, []string{"supervisor", "--task-id", "pd-test"}, runner.args)
}

func TestStartSupervisorWithEnvPassesDetachedEnv(t *testing.T) {
	runner := &fakeRunner{pid: 4242}
	pid, err := NewLauncher(runner).StartSupervisorWithEnv([]string{"OPENAI_API_KEY=secret"}, "--task-id", "pd-test")
	require.NoError(t, err)
	require.Equal(t, 4242, pid)
	exe, err := os.Executable()
	require.NoError(t, err)
	require.Equal(t, exe, runner.name)
	require.Equal(t, []string{"OPENAI_API_KEY=secret"}, runner.env)
	require.Equal(t, []string{"supervisor", "--task-id", "pd-test"}, runner.args)
}

func TestStartSupervisorWithEnvUsesConfiguredExecutable(t *testing.T) {
	runner := &fakeRunner{pid: 4242}
	pid, err := NewLauncherForExecutable(runner, "pd").StartSupervisorWithEnv(nil, "--task-id", "pd-test")
	require.NoError(t, err)
	require.Equal(t, 4242, pid)
	require.Equal(t, "pd", runner.name)
	require.Equal(t, []string{"supervisor", "--task-id", "pd-test"}, runner.args)
}

type fakeRunner struct {
	pid  int
	name string
	env  []string
	args []string
}

func (r *fakeRunner) Run(name string, args ...string) ([]byte, error)         { return nil, nil }
func (r *fakeRunner) RunDir(dir, name string, args ...string) ([]byte, error) { return nil, nil }
func (r *fakeRunner) Start(name string, args ...string) (int, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.pid, nil
}
func (r *fakeRunner) StartEnv(env []string, name string, args ...string) (int, error) {
	r.name = name
	r.env = append([]string(nil), env...)
	r.args = append([]string(nil), args...)
	return r.pid, nil
}
func (r *fakeRunner) StartPiped(name string, args ...string) (pdexec.Process, error) { return nil, nil }
