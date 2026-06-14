package main

import (
	"context"
	"fmt"
	"os"

	sbsandbox "github.com/averycrespi/agent-tools/sandbox-manager/pkg/sandbox"
)

type sandboxClient interface {
	Exec(context.Context, string, ...string) ([]byte, error)
}

type sandboxClientAdapter struct {
	client *sbsandbox.Client
}

func (a sandboxClientAdapter) Exec(_ context.Context, workdir string, args ...string) ([]byte, error) {
	return a.client.Exec(workdir, args...)
}

var (
	validateArtifactParent  = defaultValidateArtifactParent
	defaultNewSandboxClient = func() (sandboxClient, error) {
		client, err := sbsandbox.New()
		if err != nil {
			return nil, err
		}
		return sandboxClientAdapter{client: client}, nil
	}
	newSandboxClient = defaultNewSandboxClient
)

func defaultValidateArtifactParent(path string) error {
	if path == "" {
		return fmt.Errorf("artifact parent directory is required")
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("create artifact parent: %w", err)
	}
	client, err := newSandboxClient()
	if err != nil {
		return fmt.Errorf("create sandbox client: %w", err)
	}
	if _, err := client.Exec(context.Background(), "/", "test", "-d", path, "-a", "-w", path); err != nil {
		return fmt.Errorf("artifact parent %s is not visible and writable in sandbox at the same path: %w", path, err)
	}
	return nil
}
