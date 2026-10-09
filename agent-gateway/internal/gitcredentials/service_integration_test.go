//go:build integration

package gitcredentials

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

const installation = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type testClock struct{}

func (testClock) Now() time.Time { return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) }

type memoryBackend struct {
	fail  bool
	onSet func()
}

func (b *memoryBackend) observe(point string) error {
	if point == "before_write" && b.onSet != nil {
		b.onSet()
	}
	if b.fail && point == "before_write" {
		return keyring.ErrCustodyUnavailable
	}
	return nil
}

var template = storagefixture.New(installation)

func fixture(t *testing.T) (*Service, *memoryBackend, string) {
	t.Helper()
	s, backend, owner := fixtureWithFault(t, nil)
	return s, backend, owner.Layout().Root
}
func fixtureWithFault(t *testing.T, fault func(storage.FaultPoint) error) (*Service, *memoryBackend, *gatewaypaths.Ownership) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	var store *storage.Store
	if fault == nil {
		store, err = template.Open(t.Context(), owner)
	} else {
		store, err = storage.InitializeWithFaultInjection(t.Context(), owner, installation, fault)
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	authority, err := authorization.New(store, testClock{}, rand.Reader)
	require.NoError(t, err)
	backend := &memoryBackend{}
	require.NoError(t, keyring.SetupCustody(t.Context(), owner, store, testClock{}))
	provider, err := keyring.NewProvider(installation)
	require.NoError(t, err)
	require.NoError(t, provider.UseDatabaseCustody(t.Context(), owner, store))
	keyring.ObserveCustodyForIntegration(provider, backend.observe)
	s, err := NewService(store, keyring.NewCoordinator(provider, store, testClock{}, rand.Reader), authority, testClock{}, rand.Reader, installation)
	require.NoError(t, err)
	return s, backend, owner
}
func definition() contract.GitCredentialDefinition {
	return contract.GitCredentialDefinition{Name: "Repository access", Origin: "https://example.com", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}
}
func ref(c contract.GitCredential) contract.GitRevisionRef {
	return contract.GitRevisionRef{ID: c.ID, Revision: c.Revision}
}
func TestIntegrationAuditCredentialNamesFollowRenameAndDeletion(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, definition(), []byte("nonsecret-fixture-material"))
	require.NoError(t, err)
	target := contract.AuditTarget{Type: "git_credential", ID: created.ID}
	read := func() map[contract.AuditTarget]string {
		t.Helper()
		var names map[contract.AuditTarget]string
		require.NoError(t, s.store.View(ctx, func(tx *sql.Tx) error {
			var err error
			names, err = s.AuditTargetNamesTx(ctx, tx, []contract.AuditTarget{target})
			return err
		}))
		return names
	}
	require.Equal(t, created.Name, read()[target])
	def := definition()
	def.Name = "Renamed Git credential"
	updated, err := s.Update(ctx, created.ID, created.Revision, def)
	require.NoError(t, err)
	require.Equal(t, def.Name, read()[target])
	require.NoError(t, s.Delete(ctx, updated.ID, updated.Revision))
	require.Empty(t, read())
}

func TestIntegrationGitCredentialPinningPrivacyAndNoHTTPReuse(t *testing.T) {
	s, _, root := fixture(t)
	ctx := audit.WithSystem(t.Context())
	input := []byte("first-git-private-canary")
	created, err := s.Create(ctx, definition(), input)
	require.NoError(t, err)
	require.Equal(t, make([]byte, len(input)), input)
	require.True(t, created.Available)
	old, err := s.Acquire(ctx, ref(created))
	require.NoError(t, err)
	defer old.Clear()
	rotated, err := s.Rotate(ctx, created.ID, created.Revision, []byte("second-git-private-canary"))
	require.NoError(t, err)
	require.Equal(t, created.ID, rotated.ID)
	require.True(t, rotated.Available)
	require.ErrorIs(t, s.ConfirmMaterial(ctx, ref(created), old.Generation(), func() error { t.Fatal("stale material confirmed"); return nil }), ErrUnavailable)
	_, err = s.Acquire(ctx, ref(created))
	require.ErrorIs(t, err, ErrUnavailable)
	current, err := s.Acquire(ctx, ref(rotated))
	require.NoError(t, err)
	defer current.Clear()
	headers, err := current.Apply("https://example.com/team/repo", http.Header{"authorization": []string{"untrusted"}})
	require.NoError(t, err)
	require.Equal(t, "Bearer second-git-private-canary", headers.Get("Authorization"))
	_, err = current.Apply("https://evil.example/team/repo", nil)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = current.Apply("https://example.com/team/repo", http.Header{"Connection": []string{"authorization"}})
	require.ErrorIs(t, err, ErrInvalid)
	require.NoError(t, s.ConfirmMaterial(ctx, ref(rotated), current.Generation(), func() error { return nil }))
	_, err = s.Rotate(ctx, created.ID, created.Revision, []byte("stale-git-canary"))
	require.ErrorIs(t, err, ErrStale)
	still, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, rotated, still)
	httpRepo, err := httpcredentials.NewRepository(s.store, testClock{}, rand.Reader, s.authority)
	require.NoError(t, err)
	_, err = httpRepo.Get(ctx, created.ID)
	require.ErrorIs(t, err, httpcredentials.ErrNotFound)
	require.NoError(t, ValidateStartup(ctx, s.store))
	encoded, err := json.Marshal(still)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-canary")
	require.NoError(t, s.store.Checkpoint(ctx))
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
func TestIntegrationGitCredentialRetainedReferencesAndHTTPIdentityReject(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, definition(), []byte("reference-private-canary"))
	require.NoError(t, err)
	repo, err := s.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Repository", URL: "https://example.com/team/repo", Aliases: []string{}, CredentialID: &created.ID})
	require.NoError(t, err)
	def := created.GitCredentialDefinition
	def.Name = "Renamed"
	updated, err := s.Update(ctx, created.ID, created.Revision, def)
	require.NoError(t, err)
	require.Len(t, updated.References, 1)
	def.Origin = "https://evil.example"
	_, err = s.Update(ctx, created.ID, updated.Revision, def)
	require.ErrorIs(t, err, ErrReferenced)
	def = updated.GitCredentialDefinition
	def.Recipe.Header = "X-Access"
	_, err = s.Update(ctx, created.ID, updated.Revision, def)
	require.ErrorIs(t, err, ErrReferenced)
	require.NoError(t, s.authority.DeleteGitRepository(ctx, repo.ID, repo.Revision))
	require.ErrorIs(t, s.Delete(ctx, created.ID, updated.Revision), ErrReferenced)
	retained, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	require.True(t, retained.Available)
	require.Len(t, retained.References, 1)
	httpRepo, err := httpcredentials.NewRepository(s.store, testClock{}, rand.Reader, s.authority)
	require.NoError(t, err)
	httpService, err := httpcredentials.NewService(httpRepo, s.coordinator, installation)
	require.NoError(t, err)
	httpCredential, err := httpService.Create(ctx, httpcredentials.Definition{Name: "HTTP credential", Boundary: httpcredentials.Boundary{Host: "example.com", Port: 443}, Recipe: definition().Recipe}, []byte("http-only-private-canary"))
	require.NoError(t, err)
	_, err = s.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Other", URL: "https://example.com/team/other", Aliases: []string{}, CredentialID: &httpCredential.ID})
	require.ErrorIs(t, err, authorization.ErrConflict)
	_, err = s.Acquire(ctx, contract.GitRevisionRef{ID: httpCredential.ID, Revision: httpCredential.Revision})
	require.Error(t, err)
}
func TestIntegrationGitCredentialFencePrecedesProviderAndFailedRotationDoesNotRevive(t *testing.T) {
	s, backend, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, definition(), []byte("old-private-canary"))
	require.NoError(t, err)
	before, err := s.authority.AuthorizationRevision(ctx)
	require.NoError(t, err)
	calls := 0
	backend.onSet = func() {
		calls++
		during, err := s.Get(ctx, created.ID)
		require.NoError(t, err)
		require.False(t, during.Available)
		after, err := s.authority.AuthorizationRevision(ctx)
		require.NoError(t, err)
		require.NotEqual(t, before, after)
		// A provider call must not hold the authority gate or an SQL transaction.
		deadline, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		release, err := s.authority.AcquireGitCredentialAuthority(deadline)
		require.NoError(t, err)
		release()
	}
	backend.fail = true
	_, err = s.Rotate(ctx, created.ID, created.Revision, []byte("new-private-canary"))
	require.Error(t, err)
	require.Positive(t, calls)
	failed, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	require.False(t, failed.Available)
	require.NotEqual(t, created.Revision, failed.Revision)
	_, err = s.Acquire(ctx, ref(failed))
	require.Error(t, err)
	require.NoError(t, ValidateStartup(ctx, s.store))
	backend.onSet = nil
	backend.fail = false
	repaired, err := s.Rotate(ctx, created.ID, failed.Revision, []byte("repaired-private-canary"))
	require.NoError(t, err)
	require.True(t, repaired.Available)
}
func TestIntegrationGitCredentialCopiedBackupAndStoppedRestore(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, definition(), []byte("backup-private-canary"))
	require.NoError(t, err)
	_, err = s.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Repository", URL: "https://example.com/team/repo", Aliases: []string{}, CredentialID: &created.ID})
	require.NoError(t, err)
	stagedRoot := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(stagedRoot, 0o700))
	owner, err := gatewaypaths.Acquire(stagedRoot)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	// Ownership resolves platform temporary-directory aliases before containment checks.
	path := filepath.Join(owner.Layout().Root, "backup.db")
	require.NoError(t, s.store.BackupTo(ctx, path))
	identity, err := storage.VerifyBackup(ctx, path)
	require.NoError(t, err)
	require.NoError(t, VerifyBackup(ctx, path, identity.SchemaVersion))
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(bytes), "backup-private-canary")
	staged, err := storage.OpenReplacement(ctx, owner, path)
	require.NoError(t, err)
	defer func() { require.NoError(t, staged.Close()) }()
	require.NoError(t, ValidateStartup(ctx, staged))
	require.NoError(t, staged.Close())
	staged, err = storage.OpenReplacement(ctx, owner, path)
	require.NoError(t, err)
	require.NoError(t, ValidateStartup(ctx, staged))
	require.NoError(t, InvalidateStagedCredentials(ctx, staged, testClock{}))
	require.NoError(t, ValidateStartup(ctx, staged))
	stagedAuthority, err := authorization.New(staged, testClock{}, rand.Reader)
	require.NoError(t, err)
	provider, err := keyring.NewProvider(installation)
	require.NoError(t, err)
	restored, err := NewService(staged, keyring.NewCoordinator(provider, staged, testClock{}, rand.Reader), stagedAuthority, testClock{}, rand.Reader, installation)
	require.NoError(t, err)
	retained, err := restored.Get(ctx, created.ID)
	require.NoError(t, err)
	require.False(t, retained.Available)
	require.Equal(t, created.GitCredentialDefinition, retained.GitCredentialDefinition)
	require.Len(t, retained.References, 1)
	_, err = restored.Acquire(ctx, ref(retained))
	require.Error(t, err)
	live, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	require.True(t, live.Available)
	require.NoError(t, staged.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE git_credentials SET origin='https://evil.example:443' WHERE id=?`, created.ID)
		return err
	}))
	require.NoError(t, staged.Checkpoint(ctx))
	require.Error(t, VerifyBackup(ctx, path, identity.SchemaVersion))
}
func TestIntegrationGitCredentialMalformedStartupAndMissingMaterial(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	created, err := s.Create(ctx, definition(), []byte("missing-private-canary"))
	require.NoError(t, err)
	require.NoError(t, s.store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE secret_generations SET ciphertext=zeroblob(length(ciphertext))`)
		return err
	}))
	_, err = s.Acquire(ctx, ref(created))
	require.Error(t, err)
	require.NoError(t, s.store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE git_credentials SET header='authorization' WHERE id=?`, created.ID)
		return err
	}))
	require.Error(t, ValidateStartup(ctx, s.store))
}
