//go:build integration

package httpcredentials

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type clock struct{}

func (clock) Now() time.Time { return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) }

type memoryKeyring struct {
	mu     sync.Mutex
	values map[string]string
}

func (*memoryKeyring) Probe(context.Context, string) error { return nil }
func (m *memoryKeyring) Set(service, user, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[service+user] = password
	return nil
}
func (m *memoryKeyring) Get(service, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[service+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}
func (m *memoryKeyring) Delete(service, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, service+user)
	return nil
}

type referenceStore struct{}

func (referenceStore) ReferencesTx(ctx context.Context, tx *sql.Tx, id string) ([]Reference, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,policy FROM test_http_grant_references WHERE credential_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Reference{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		policy, err := httppolicy.DecodePolicy([]byte(raw))
		if err != nil {
			return nil, err
		}
		out = append(out, Reference{ID: id, Policy: policy})
	}
	return out, rows.Err()
}

func fixture(t *testing.T) (*Service, *storage.Store, string) {
	t.Helper()
	return fixtureWithFault(t, nil)
}

func fixtureWithFault(t *testing.T, fault func(storage.FaultPoint) error) (*Service, *storage.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	store, err := storage.InitializeWithFaultInjection(t.Context(), owner, "01ARZ3NDEKTSV4RRFFQ69G5FAV", fault)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TABLE test_http_grant_references(id TEXT PRIMARY KEY,credential_id TEXT,policy TEXT)`)
		return err
	}))
	provider, err := keyring.NewProviderWithBackend("01ARZ3NDEKTSV4RRFFQ69G5FAV", &memoryKeyring{values: map[string]string{}})
	require.NoError(t, err)
	repo, err := NewRepository(store, clock{}, rand.Reader, referenceStore{})
	require.NoError(t, err)
	service, err := NewService(repo, keyring.NewCoordinator(provider, store, clock{}, rand.Reader), "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	return service, store, root
}
func resourceRef(t *testing.T, r Resource) contract.HTTPRevisionRef {
	t.Helper()
	revision, err := strconv.ParseUint(r.Revision, 10, 64)
	require.NoError(t, err)
	return contract.HTTPRevisionRef{ID: r.ID, Revision: revision}
}

func TestIntegrationCredentialEscapedSecretAtBound(t *testing.T) {
	for _, character := range []string{"<", `\`} {
		t.Run(character, func(t *testing.T) {
			s, _, _ := fixture(t)
			ctx := audit.WithSystem(t.Context())
			definition := testDefinition()
			definition.Recipe.Prefix = ""
			secret := strings.Repeat(character, contract.HTTPCredentialValueBytes)
			created, err := s.Create(ctx, definition, []byte(secret))
			require.NoError(t, err)
			material, err := s.Acquire(ctx, resourceRef(t, created))
			require.NoError(t, err)
			defer material.Clear()
			target, err := httppolicy.ParseRequest("https://api.example.com/", "GET", "api.example.com", "", nil)
			require.NoError(t, err)
			headers, err := material.Headers(target, nil)
			require.NoError(t, err)
			require.Equal(t, secret, headers.Get("Authorization"))
		})
	}
}

func TestIntegrationCredentialRotationPinsMaterialAndPreservesIdentity(t *testing.T) {
	s, store, root := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, testDefinition(), []byte("first-private-canary"))
	require.NoError(t, err)
	require.True(t, created.Available)
	old, err := s.Acquire(ctx, resourceRef(t, created))
	require.NoError(t, err)
	defer old.Clear()
	rotated, err := s.Rotate(ctx, created.ID, created.Revision, []byte("second-private-canary"))
	require.NoError(t, err)
	require.Equal(t, created.ID, rotated.ID)
	require.NotEqual(t, created.Revision, rotated.Revision)
	_, err = s.Acquire(ctx, resourceRef(t, created))
	require.ErrorIs(t, err, ErrUnavailable)
	current, err := s.Acquire(ctx, resourceRef(t, rotated))
	require.NoError(t, err)
	defer current.Clear()
	target, err := httppolicy.ParseRequest("https://api.example.com/", "GET", "api.example.com", "", nil)
	require.NoError(t, err)
	oldHeaders, err := old.Headers(target, nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer first-private-canary", oldHeaders.Get("Authorization"))
	newHeaders, err := current.Headers(target, nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer second-private-canary", newHeaders.Get("Authorization"))
	_, err = s.Rotate(ctx, created.ID, created.Revision, []byte("stale-canary"))
	require.ErrorIs(t, err, ErrStale)
	still, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, rotated.Revision, still.Revision)
	require.True(t, still.Available)
	require.NoError(t, ValidateStartup(ctx, store))
	encoded, err := json.Marshal(still)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-canary")
	require.NoError(t, store.Checkpoint(ctx))
	for _, file := range []string{"gateway.db", "gateway.db-wal"} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		require.NotContains(t, string(data), "private-canary")
	}
	require.NoError(t, s.Delete(ctx, created.ID, rotated.Revision))
	_, err = s.Get(ctx, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestIntegrationCredentialReferencesAreCheckedOnMutationTransaction(t *testing.T) {
	s, store, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	def := testDefinition()
	def.Boundary.Host = "*.example.com"
	def.Boundary.AllowWildcard = true
	created, err := s.Create(ctx, def, []byte("private-canary"))
	require.NoError(t, err)
	policy := contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowRequests, CredentialID: &created.ID, Request: &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: "https", Host: "api.example.com", Port: 443}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}}
	compiled, err := httppolicy.Compile(policy)
	require.NoError(t, err)
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		if err := s.repository.CheckReferenceTx(ctx, tx, created.ID, compiled); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO test_http_grant_references VALUES(?,?,?)`, "01ARZ3NDEKTSV4RRFFQ69G5FAW", created.ID, string(raw))
		return err
	}))
	bad := testDefinition()
	bad.Boundary.Host = "other.example.com"
	_, err = s.Update(ctx, created.ID, created.Revision, bad)
	require.ErrorIs(t, err, ErrReferenced)
	require.ErrorIs(t, s.Delete(ctx, created.ID, created.Revision), ErrReferenced)
	unchanged, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Revision, unchanged.Revision)
	require.True(t, unchanged.Available)
	require.Len(t, unchanged.References, 1)
	updated, err := s.Update(ctx, created.ID, created.Revision, testDefinition())
	require.NoError(t, err)
	require.Equal(t, "api.example.com", updated.Boundary.Host)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM test_http_grant_references`)
		return err
	}))
	require.NoError(t, s.Delete(ctx, created.ID, updated.Revision))
	err = store.Mutate(ctx, func(tx *sql.Tx) error { return s.repository.CheckReferenceTx(ctx, tx, created.ID, compiled) })
	require.ErrorIs(t, err, ErrNotFound)
}

func TestIntegrationRestoredHTTPAuthorityCannotUseSurvivingKeyringMaterial(t *testing.T) {
	s, store, _ := fixture(t)
	ctx := audit.WithOffline(t.Context())
	created, err := s.Create(ctx, testDefinition(), []byte("restored-private-canary"))
	require.NoError(t, err)
	require.NoError(t, InvalidateStagedCredentials(ctx, store, clock{}))
	restored, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	require.False(t, restored.Available)
	_, err = s.Acquire(ctx, resourceRef(t, restored))
	require.Error(t, err)
	rotated, err := s.Rotate(ctx, restored.ID, restored.Revision, []byte("new-authorized-canary"))
	require.NoError(t, err)
	require.True(t, rotated.Available)
}
