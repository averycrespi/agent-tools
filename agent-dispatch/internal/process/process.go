package process

import (
	"errors"
	"syscall"

	adexec "github.com/averycrespi/agent-tools/agent-dispatch/internal/exec"
)

type Launcher struct{ runner adexec.Runner }

func NewLauncher(runner adexec.Runner) *Launcher { return &Launcher{runner: runner} }

func (l *Launcher) StartSupervisor(args ...string) (int, error) {
	return l.runner.Start("ad", append([]string{"supervisor"}, args...)...)
}

func Exists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
