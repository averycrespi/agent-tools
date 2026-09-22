//go:build integration

package httpca

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type memoryBackend struct {
	mu     sync.Mutex
	values map[string]string
	fail   bool
}

func (*memoryBackend) Probe(context.Context, string) error { return nil }
func (m *memoryBackend) Set(service, user, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return keyring.ErrNotFound
	}
	m.values[service+user] = value
	return nil
}
func (m *memoryBackend) Get(service, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[service+user]
	if !ok || m.fail {
		return "", keyring.ErrNotFound
	}
	return v, nil
}
func (m *memoryBackend) Delete(_, _ string) error { return nil } // Retired physical keys deliberately survive.

func TestIntegrationCAStableRestartRestoreAndKeyLoss(t *testing.T) {
	ctx := t.Context()
	const installation = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	store, err := storage.Initialize(ctx, owner, installation)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	backend := &memoryBackend{values: map[string]string{}}
	provider, err := keyring.NewProviderWithBackend(installation, backend)
	require.NoError(t, err)
	c := &testClock{time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)}
	makeService := func() *Service {
		s, err := New(store, keyring.NewCoordinator(provider, store, c, rand.Reader), installation, c, rand.Reader)
		require.NoError(t, err)
		return s
	}
	s := makeService()
	_, err = s.Load(ctx)
	require.Error(t, err)
	require.Empty(t, backend.values)
	require.NoError(t, s.Replace(ctx, "0"))
	require.NoError(t, ValidateStartup(ctx, store))
	public, revision, err := s.PublicCertificate(ctx)
	require.NoError(t, err)
	signer, err := s.Load(ctx)
	require.NoError(t, err)
	leaf, err := signer.Certificate("example.com")
	require.NoError(t, err)
	s.Close()
	s = makeService()
	again, rev, err := s.PublicCertificate(ctx)
	require.NoError(t, err)
	require.Equal(t, public, again)
	require.Equal(t, revision, rev)
	_, err = s.Load(ctx)
	require.NoError(t, err)
	// A restore invalidates even when all old physical chunks remain readable.
	s.Close()
	require.NoError(t, InvalidateStaged(ctx, store, c))
	require.NoError(t, ValidateStartup(ctx, store))
	s = makeService()
	_, err = s.Load(ctx)
	require.Error(t, err)
	var current uint64
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error { return tx.QueryRowContext(ctx, `SELECT revision FROM http_ca`).Scan(&current) }))
	_, rev, err = s.PublicCertificate(ctx)
	require.NoError(t, err)
	require.NoError(t, s.Replace(ctx, rev))
	replacement, rev, err := s.PublicCertificate(ctx)
	require.NoError(t, err)
	require.NotEqual(t, public, replacement)
	signer, err = s.Load(ctx)
	require.NoError(t, err)
	require.Error(t, leaf.Leaf.CheckSignatureFrom(signer.root))
	s.Close()
	backend.fail = true
	s = makeService()
	_, err = s.Load(ctx)
	require.Error(t, err)
	before := len(backend.values)
	require.Error(t, s.Replace(ctx, rev))
	require.Len(t, backend.values, before)
	backend.fail = false
	_, err = s.Load(ctx)
	require.Error(t, err) // No old-key fallback after failed explicit cutover.
}
