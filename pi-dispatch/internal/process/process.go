package process

import (
	"errors"
	"syscall"

	pdexec "github.com/averycrespi/agent-tools/pi-dispatch/internal/exec"
)

type Launcher struct{ runner pdexec.Runner }

func NewLauncher(runner pdexec.Runner) *Launcher { return &Launcher{runner: runner} }

func (l *Launcher) StartSupervisor(args ...string) (int, error) {
	return l.runner.Start("pd", append([]string{"supervisor"}, args...)...)
}

func Exists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
