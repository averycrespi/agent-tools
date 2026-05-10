package exec

import osexec "os/exec"

type Runner interface {
	Run(name string, args ...string) ([]byte, error)
	Start(name string, args ...string) error
}

type OSRunner struct{}

func NewOSRunner() *OSRunner { return &OSRunner{} }

func (r *OSRunner) Run(name string, args ...string) ([]byte, error) {
	return osexec.Command(name, args...).CombinedOutput() //nolint:gosec
}

func (r *OSRunner) Start(name string, args ...string) error {
	return osexec.Command(name, args...).Start() //nolint:gosec
}
