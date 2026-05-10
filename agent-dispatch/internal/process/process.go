package process

import adexec "github.com/averycrespi/agent-tools/agent-dispatch/internal/exec"

type Launcher struct{ runner adexec.Runner }

func NewLauncher(runner adexec.Runner) *Launcher { return &Launcher{runner: runner} }

func (l *Launcher) StartSupervisor(args ...string) error {
	return l.runner.Start("ad", append([]string{"supervisor"}, args...)...)
}
