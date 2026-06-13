package main

import (
	"fmt"
	"os"

	sbsandbox "github.com/averycrespi/agent-tools/sandbox-manager/pkg/sandbox"
)

var validateArtifactParent = defaultValidateArtifactParent

func defaultValidateArtifactParent(path string) error {
	if path == "" {
		return fmt.Errorf("artifact parent directory is required")
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("create artifact parent: %w", err)
	}
	client, err := sbsandbox.New()
	if err != nil {
		return fmt.Errorf("create sandbox client: %w", err)
	}
	if _, err := client.Exec("/", "test", "-d", path); err != nil {
		return fmt.Errorf("artifact parent %s is not visible in sandbox at the same path: %w", path, err)
	}
	return nil
}
