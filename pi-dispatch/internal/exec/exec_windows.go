//go:build windows

package exec

import "os/exec"

func detachCommand(cmd *exec.Cmd) {}
