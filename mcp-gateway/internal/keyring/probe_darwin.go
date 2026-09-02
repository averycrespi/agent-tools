//go:build darwin

package keyring

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"

	oskeyring "github.com/zalando/go-keyring"
)

const securityTool = "/usr/bin/security"

func probeSystem(ctx context.Context, _ string) error {
	err := probeDarwin(ctx, execSecurityProbeRunner{})
	if errors.Is(err, errSecurityToolMissing) {
		return oskeyring.ErrUnsupportedPlatform
	}
	return err
}

type execSecurityProbeRunner struct{}

func (execSecurityProbeRunner) Run(ctx context.Context, arguments ...string) error {
	command := exec.CommandContext(ctx, securityTool, arguments...)
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
			return errSecurityToolMissing
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(arguments) > 0 {
			return classifySecurityExit(arguments[0], exitErr.ExitCode())
		}
		return err
	}
	return nil
}
