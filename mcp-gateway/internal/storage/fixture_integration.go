//go:build integration

package storage

import (
	"context"
	"fmt"
	"os"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
)

// WriteAcceptedSchemaFixtureForIntegration writes a stopped historical generation for cross-package restore tests.
func WriteAcceptedSchemaFixtureForIntegration(ctx context.Context, root, installationID string, version int) (string, error) {
	if version < 3 || version > CurrentSchema || !installationIDPattern.MatchString(installationID) {
		return "", ErrInvalidDatabase
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return "", fmt.Errorf("create integration fixture root: %w", err)
	}
	ownership, err := gatewaypaths.Acquire(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = ownership.Close() }()
	layout := ownership.Layout()
	file, err := gatewaypaths.CreateOwnerOnlyFile(layout.Database)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	store, err := openConfigured(ctx, layout, testOptions{})
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		_ = store.Close()
		if !complete {
			removeDatabaseGeneration(layout.Database)
		}
	}()
	if err := store.bootstrap(ctx, installationID); err != nil {
		return "", err
	}
	if err := store.configureSizeLimit(ctx); err != nil {
		return "", err
	}
	if err := store.migrateThrough(ctx, 0, version); err != nil {
		return "", err
	}
	if err := store.Checkpoint(ctx); err != nil {
		return "", err
	}
	if err := store.Close(); err != nil {
		return "", err
	}
	complete = true
	return layout.Database, nil
}
