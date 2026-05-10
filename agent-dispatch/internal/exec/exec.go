package exec

import (
	"io"
	oexec "os/exec"
)

type Runner interface {
	Run(name string, args ...string) ([]byte, error)
	RunDir(dir, name string, args ...string) ([]byte, error)
	Start(name string, args ...string) error
	StartPiped(name string, args ...string) (Process, error)
}

type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Kill() error
}

type osProcess struct {
	cmd    *oexec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (p *osProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *osProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *osProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *osProcess) Wait() error           { return p.cmd.Wait() }
func (p *osProcess) Kill() error           { return p.cmd.Process.Kill() }

type OSRunner struct{}

func NewOSRunner() *OSRunner { return &OSRunner{} }

func (r *OSRunner) Run(name string, args ...string) ([]byte, error) {
	return oexec.Command(name, args...).CombinedOutput() //nolint:gosec
}

func (r *OSRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	cmd := oexec.Command(name, args...) //nolint:gosec
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (r *OSRunner) Start(name string, args ...string) error {
	return oexec.Command(name, args...).Start() //nolint:gosec
}

func (r *OSRunner) StartPiped(name string, args ...string) (Process, error) {
	cmd := oexec.Command(name, args...) //nolint:gosec
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}
