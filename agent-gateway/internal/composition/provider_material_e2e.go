//go:build e2e

package composition

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
)

// A disposable fake keyring, outside the installation and its backup/evidence
// trees. Only a separately linked fixture binary can select this plaintext
// test store. It qualifies cross-process composition, never native key custody.
type e2eFileBackend struct {
	directory string
	mu        sync.Mutex
}

func newE2EFileBackend(directory string) (*e2eFileBackend, error) {
	info, err := os.Lstat(directory)
	if err != nil || !filepath.IsAbs(directory) || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("invalid E2E material fixture")
	}
	marker, err := os.ReadFile(filepath.Join(directory, ".fixture"))
	if err != nil || string(marker) != "agent-gateway-disposable-e2e-material\n" {
		return nil, errors.New("missing E2E material fixture marker")
	}
	return &e2eFileBackend{directory: directory}, nil
}
func (b *e2eFileBackend) name(service, user string) string {
	return filepath.Join(b.directory, fmt.Sprintf("%x", sha256.Sum256([]byte(service+"\x00"+user))))
}
func (*e2eFileBackend) Probe(context.Context, string) error { return nil }
func (b *e2eFileBackend) Set(service, user, password string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(password) > 64*1024 {
		return errors.New("E2E material exceeds bound")
	}
	root, err := os.OpenRoot(b.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(b.name(service, user))
	if info, err := root.Lstat(name); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600) {
		return errors.New("unsafe E2E material")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.WriteFile(name, []byte(password), 0o600)
}
func (b *e2eFileBackend) Get(service, user string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	root, err := os.OpenRoot(b.directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := filepath.Base(b.name(service, user))
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return "", keyring.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 64*1024 {
		return "", errors.New("unsafe E2E material")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if len(value) > 64*1024 {
		return "", errors.New("E2E material exceeds bound")
	}
	return string(value), err
}
func (b *e2eFileBackend) Delete(service, user string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	err := os.Remove(b.name(service, user))
	if errors.Is(err, os.ErrNotExist) {
		return keyring.ErrNotFound
	}
	return err
}
