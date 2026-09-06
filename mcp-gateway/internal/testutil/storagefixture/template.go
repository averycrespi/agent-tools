package storagefixture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

type Template struct {
	image func() (string, error)
}

func New(installationID string) *Template {
	return &Template{image: sync.OnceValues(func() (string, error) {
		return initializeImage(installationID)
	})}
}

func (template *Template) Open(ctx context.Context, ownership *gatewaypaths.Ownership) (*storage.Store, error) {
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return nil, err
	}
	image, err := template.image()
	if err != nil {
		return nil, err
	}
	file, err := gatewaypaths.CreateOwnerOnlyFile(layout.Database)
	if err != nil {
		return nil, err
	}
	_, copyErr := io.Copy(file, strings.NewReader(image))
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return nil, fmt.Errorf("copy fixture database: %w", err)
	}
	return storage.Open(ctx, ownership)
}

func initializeImage(installationID string) (image string, resultErr error) {
	root, err := os.MkdirTemp("", "mcp-gateway-schema-fixture-")
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(root)) }()
	ownership, err := gatewaypaths.Acquire(root)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, ownership.Close()) }()
	store, err := storage.Initialize(context.Background(), ownership, installationID)
	if err != nil {
		return "", err
	}
	if err := store.Close(); err != nil {
		return "", err
	}
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return "", err
	}
	// Only a closed, checkpointed generation can become an immutable process-local image.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(layout.Database + suffix); !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("fixture database has a sidecar or inspection failure: %s: %w", suffix, err)
		}
	}
	contents, err := os.ReadFile(layout.Database)
	return string(contents), err
}
