package process

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	pdexec "github.com/averycrespi/agent-tools/pi-dispatch/internal/exec"
)

type Launcher struct{ runner pdexec.Runner }

func NewLauncher(runner pdexec.Runner) *Launcher { return &Launcher{runner: runner} }

func (l *Launcher) StartSupervisor(args ...string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	return l.runner.Start(exe, append([]string{"supervisor"}, args...)...)
}

func (l *Launcher) StartSupervisorWithEnv(env []string, args ...string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	runner, ok := l.runner.(interface {
		StartEnv(env []string, name string, args ...string) (int, error)
	})
	if !ok {
		return 0, fmt.Errorf("runner does not support environment overrides")
	}
	return runner.StartEnv(env, exe, append([]string{"supervisor"}, args...)...)
}

func Exists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// KillGroup sends SIGKILL to the process group led by pid. Supervisors are
// started with Setpgid so the group includes the supervisor, its limactl
// shell, and any other descendants.
func KillGroup(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill process group %d: %w", pid, err)
	}
	return nil
}
