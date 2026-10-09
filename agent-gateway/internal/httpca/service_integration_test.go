//go:build integration

package httpca

import (
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

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
	c := &testClock{time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)}
	require.NoError(t, keyring.SetupCustody(ctx, owner, store, c))
	provider, err := keyring.NewProvider(installation)
	require.NoError(t, err)
	require.NoError(t, provider.UseDatabaseCustody(ctx, owner, store))
	fail := false
	keyring.ObserveCustodyForIntegration(provider, func(string) error {
		if fail {
			return keyring.ErrIncompleteGeneration
		}
		return nil
	})
	count := func() int {
		var n int
		require.NoError(t, store.View(ctx, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT count(*) FROM secret_generations`).Scan(&n) }))
		return n
	}
	makeService := func() *Service {
		s, err := New(store, keyring.NewCoordinator(provider, store, c, rand.Reader), installation, c, rand.Reader)
		require.NoError(t, err)
		return s
	}
	s := makeService()
	_, err = s.Load(ctx)
	require.Error(t, err)
	require.Zero(t, count())
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
	// Explicit staged invalidation cannot reuse retained ciphertext as authority.
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
	fail = true
	s = makeService()
	_, err = s.Load(ctx)
	require.Error(t, err)
	before := count()
	require.Error(t, s.Replace(ctx, rev))
	require.Equal(t, before, count())
	fail = false
	_, err = s.Load(ctx)
	require.Error(t, err) // No old-key fallback after failed explicit cutover.
}
