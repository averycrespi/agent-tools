package exec

import (
	"io"
	"os"
	osexec "os/exec"
)

// Runner abstracts command execution for testability.
type Runner interface {
	// Run executes a command and returns its combined output.
	Run(name string, args ...string) ([]byte, error)

	// RunInteractive executes a command with stdin/stdout/stderr connected.
	RunInteractive(name string, args ...string) error

	// StartPiped starts a command with stdin/stdout/stderr pipes.
	StartPiped(name string, args ...string) (Process, error)
}

// Process is a started command with streaming stdio.
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Kill() error
}

type osProcess struct {
	cmd    *osexec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (p *osProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *osProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *osProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *osProcess) Wait() error           { return p.cmd.Wait() }
func (p *osProcess) Kill() error           { return p.cmd.Process.Kill() }

// OSRunner implements Runner using os/exec.
type OSRunner struct{}

// NewOSRunner returns a new OSRunner.
func NewOSRunner() *OSRunner { return &OSRunner{} }

// Run executes a command and returns combined stdout+stderr.
func (r *OSRunner) Run(name string, args ...string) ([]byte, error) {
	return osexec.Command(name, args...).CombinedOutput() //nolint:gosec
}

// RunInteractive executes a command with stdin/stdout/stderr attached.
func (r *OSRunner) RunInteractive(name string, args ...string) error {
	cmd := osexec.Command(name, args...) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// StartPiped starts a command with separate stdin/stdout/stderr pipes.
func (r *OSRunner) StartPiped(name string, args ...string) (Process, error) {
	cmd := osexec.Command(name, args...) //nolint:gosec
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
