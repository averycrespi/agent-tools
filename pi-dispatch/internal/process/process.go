package process

import (
	"errors"
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

func Exists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
