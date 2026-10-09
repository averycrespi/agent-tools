//go:build integration

package keyring_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type cleanupProducerClock struct{}

func (cleanupProducerClock) Now() time.Time { return time.Unix(1700000000, 0).UTC() }

type cleanupProducerError struct {
	text  string
	check func()
}

func (e *cleanupProducerError) Error() string { e.check(); return e.text }

type cleanupProducerFixture struct {
	ctx         context.Context
	store       *storage.Store
	provider    *keyring.Provider
	coordinator *keyring.Coordinator
	adapter     *diagnostics.Adapter
	output      bytes.Buffer
}

func newCleanupProducerFixture(t *testing.T) *cleanupProducerFixture {
	t.Helper()
	f := &cleanupProducerFixture{ctx: audit.WithSystem(t.Context())}
	owner, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	f.store, err = storage.Initialize(f.ctx, owner, serverTestInstallationID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.store.Close()) })
	require.NoError(t, keyring.SetupCustody(f.ctx, owner, f.store, cleanupProducerClock{}))
	f.provider, err = keyring.NewProvider(serverTestInstallationID)
	require.NoError(t, err)
	require.NoError(t, f.provider.UseDatabaseCustody(f.ctx, owner, f.store))
	f.coordinator = keyring.NewCoordinator(f.provider, f.store, cleanupProducerClock{}, rand.Reader)
	f.adapter = diagnostics.New(&f.output, diagnostics.Warn)
	t.Cleanup(func() { f.adapter.Finish(nil); <-f.adapter.Done() })
	f.coordinator.SetDiagnostics(f.adapter)
	return f
}

// Stage an unrelated retained handle after the real encrypted write. Cleanup
// can then fail only after activation, without blocking the pre-cutover sweep.
func (f *cleanupProducerFixture) failAfterActivation(t *testing.T, cause error) *string {
	t.Helper()
	ownerID := new(string)
	armed := false
	keyring.ObserveCustodyForIntegration(f.provider, func(point string) error {
		if point == "after_write" {
			handle, err := keyring.NewHandle(rand.Reader)
			require.NoError(t, err)
			require.NoError(t, f.store.Mutate(f.ctx, func(tx *sql.Tx) error {
				var kind string
				if err := tx.QueryRow(`SELECT owner,kind FROM keyring_candidates LIMIT 1`).Scan(ownerID, &kind); err != nil {
					return err
				}
				_, err := tx.Exec(`INSERT INTO keyring_candidates(owner,kind,handle,created_at) VALUES(?,?,?,?)`, *ownerID, kind, string(handle), cleanupProducerClock{}.Now())
				return err
			}))
			armed = true
		}
		if point == "before_delete" && armed {
			return cause
		}
		return nil
	})
	return ownerID
}

func TestIntegrationHTTPCredentialCleanupMasksValuesAfterMutationAdmission(t *testing.T) {
	for _, action := range []string{"create", "rotate", "delete"} {
		t.Run(action, func(t *testing.T) {
			f := newCleanupProducerFixture(t)
			authority, err := authorization.New(f.store, cleanupProducerClock{}, rand.Reader)
			require.NoError(t, err)
			repository, err := httpcredentials.NewRepository(f.store, cleanupProducerClock{}, rand.Reader, authority)
			require.NoError(t, err)
			service, err := httpcredentials.NewService(repository, f.coordinator, serverTestInstallationID)
			require.NoError(t, err)
			definition := httpcredentials.Definition{Name: "diagnostic", Boundary: httpcredentials.Boundary{Host: "example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}
			var current httpcredentials.Resource
			if action != "create" {
				current, err = service.Create(f.ctx, definition, []byte("prior-value"))
				require.NoError(t, err)
			}
			var ownerID *string
			formatted := 0
			cause := &cleanupProducerError{text: "native retained delete failure actual-http-canary", check: func() {
				formatted++
				_, probeErr := service.Rotate(f.ctx, *ownerID, "1", nil)
				require.NotErrorIs(t, probeErr, httpcredentials.ErrUnavailable, "outer mutation admission must be released before formatting")
				require.Error(t, probeErr)
			}}
			if action == "delete" {
				ownerID = &current.ID
				cause.text = "native retained delete failure"
				keyring.ObserveCustodyForIntegration(f.provider, func(point string) error {
					if point == "before_delete" {
						return cause
					}
					return nil
				})
				err = service.Delete(f.ctx, current.ID, current.Revision)
			} else {
				ownerID = f.failAfterActivation(t, cause)
				if action == "create" {
					current, err = service.Create(f.ctx, definition, []byte("actual-http-canary"))
				} else {
					current, err = service.Rotate(f.ctx, current.ID, current.Revision, []byte("actual-http-canary"))
				}
			}
			require.NoError(t, err)
			require.Positive(t, formatted)
			if action != "delete" {
				selected, readErr := service.Get(f.ctx, current.ID)
				require.NoError(t, readErr)
				require.True(t, selected.Available)
				ns, nsErr := keyring.NewNamespace(serverTestInstallationID, current.ID, keyring.RecordHTTPCredential)
				require.NoError(t, nsErr)
				value, _, readErr := f.coordinator.ReadActive(f.ctx, ns)
				require.NoError(t, readErr)
				require.Contains(t, string(value), "actual-http-canary")
				clear(value)
			} else {
				_, readErr := service.Get(f.ctx, current.ID)
				require.ErrorIs(t, readErr, httpcredentials.ErrNotFound)
			}
			require.True(t, f.adapter.Finish(nil))
			require.Contains(t, f.output.String(), "native retained delete failure")
			require.Contains(t, f.output.String(), "cleanup=unconfirmed")
			require.NotContains(t, f.output.String(), "actual-http-canary")
		})
	}
}

func TestIntegrationGitCredentialCleanupMasksValuesAfterMutationAdmission(t *testing.T) {
	for _, action := range []string{"create", "rotate", "delete"} {
		t.Run(action, func(t *testing.T) {
			f := newCleanupProducerFixture(t)
			authority, err := authorization.New(f.store, cleanupProducerClock{}, rand.Reader)
			require.NoError(t, err)
			service, err := gitcredentials.NewService(f.store, f.coordinator, authority, cleanupProducerClock{}, rand.Reader, serverTestInstallationID)
			require.NoError(t, err)
			definition := contract.GitCredentialDefinition{Name: "diagnostic", Origin: "https://example.com", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}
			var current contract.GitCredential
			if action != "create" {
				current, err = service.Create(f.ctx, definition, []byte("prior-value"))
				require.NoError(t, err)
			}
			var ownerID *string
			formatted := 0
			cause := &cleanupProducerError{text: "native retained delete failure actual-git-canary", check: func() {
				formatted++
				_, probeErr := service.Rotate(f.ctx, *ownerID, "1", nil)
				require.NotErrorIs(t, probeErr, gitcredentials.ErrUnavailable, "outer mutation admission must be released before formatting")
				require.Error(t, probeErr)
			}}
			if action == "delete" {
				ownerID = &current.ID
				cause.text = "native retained delete failure"
				keyring.ObserveCustodyForIntegration(f.provider, func(point string) error {
					if point == "before_delete" {
						return cause
					}
					return nil
				})
				err = service.Delete(f.ctx, current.ID, current.Revision)
			} else {
				ownerID = f.failAfterActivation(t, cause)
				if action == "create" {
					current, err = service.Create(f.ctx, definition, []byte("actual-git-canary"))
				} else {
					current, err = service.Rotate(f.ctx, current.ID, current.Revision, []byte("actual-git-canary"))
				}
			}
			require.NoError(t, err)
			require.Positive(t, formatted)
			if action != "delete" {
				selected, readErr := service.Get(f.ctx, current.ID)
				require.NoError(t, readErr)
				require.True(t, selected.Available)
				ns, nsErr := keyring.NewNamespace(serverTestInstallationID, current.ID, keyring.RecordGitCredential)
				require.NoError(t, nsErr)
				value, _, readErr := f.coordinator.ReadActive(f.ctx, ns)
				require.NoError(t, readErr)
				require.Contains(t, string(value), "actual-git-canary")
				clear(value)
			} else {
				_, readErr := service.Get(f.ctx, current.ID)
				require.ErrorIs(t, readErr, gitcredentials.ErrNotFound)
			}
			require.True(t, f.adapter.Finish(nil))
			require.Contains(t, f.output.String(), "native retained delete failure")
			require.Contains(t, f.output.String(), "cleanup=unconfirmed")
			require.NotContains(t, f.output.String(), "actual-git-canary")
		})
	}
}

func TestIntegrationCACleanupMasksGeneratedKeyAfterMutationAdmission(t *testing.T) {
	f := newCleanupProducerFixture(t)
	service, err := httpca.New(f.store, f.coordinator, serverTestInstallationID, cleanupProducerClock{}, rand.Reader)
	require.NoError(t, err)
	t.Cleanup(service.Close)
	var keyCanary string
	formatted := 0
	cause := &cleanupProducerError{}
	cause.check = func() {
		formatted++
		signer, loadErr := service.Load(f.ctx)
		require.NoError(t, loadErr, "CA mutation admission must be released before formatting")
		require.NotNil(t, signer)
		ns, nsErr := keyring.NewNamespace(serverTestInstallationID, serverTestInstallationID, keyring.RecordHTTPCA)
		require.NoError(t, nsErr)
		payload, _, readErr := f.coordinator.ReadActive(f.ctx, ns)
		require.NoError(t, readErr)
		var generation struct {
			Key string `json:"key"`
		}
		require.NoError(t, json.Unmarshal(payload, &generation))
		clear(payload)
		keyCanary = generation.Key
		cause.text = "native retained delete failure " + keyCanary
	}
	f.failAfterActivation(t, cause)
	require.NoError(t, service.Replace(f.ctx, "0"))
	require.Positive(t, formatted)
	require.NotEmpty(t, keyCanary)
	certificate, revision, err := service.PublicCertificate(f.ctx)
	require.NoError(t, err)
	require.NotEmpty(t, certificate)
	require.Equal(t, "1", revision)
	require.True(t, f.adapter.Finish(nil))
	require.Contains(t, f.output.String(), "native retained delete failure")
	require.Contains(t, f.output.String(), "cleanup=unconfirmed")
	require.NotContains(t, f.output.String(), keyCanary)
}

func TestIntegrationStaticReplacementCleanupMasksConstituentValues(t *testing.T) {
	f := newCleanupProducerFixture(t)
	repository, err := servers.New(f.store, cleanupProducerClock{}, rand.Reader)
	require.NoError(t, err)
	created, err := repository.Create(f.ctx, servers.CreateRequest{Definition: servers.Definition{Namespace: "cleanup", DisplayName: "Cleanup", Enabled: false, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "token", "OTHER": "other"}}}, Idempotency: &servers.IdempotencyRequest{AuthorityID: serverTestInstallationID, Method: "POST", Route: "/api/v2/mcp/servers", Key: "cleanup-create", RequestHash: sha256.Sum256([]byte("cleanup-create"))}})
	require.NoError(t, err)
	service, err := servercredentials.New(repository, f.coordinator, serverTestInstallationID, nil, nil)
	require.NoError(t, err)
	plan, err := service.Prepare(f.ctx, servers.CredentialReplacementRequest{ServerID: created.Server.ID, Kind: contract.ServerCredentialStatic, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0", Slots: []string{"other", "token"}})
	require.NoError(t, err)
	payload, err := servercredentials.EncodeStaticGeneration(map[string]string{"token": "actual-static-canary", "other": "second-static-canary"})
	require.NoError(t, err)
	formatted := 0
	f.failAfterActivation(t, &cleanupProducerError{text: "native retained delete failure actual-static-canary second-static-canary", check: func() { formatted++ }})
	publication, err := service.Replace(f.ctx, plan, payload)
	require.NoError(t, err)
	require.Equal(t, "1", publication.Revision)
	require.Positive(t, formatted)
	ns, err := keyring.NewNamespace(serverTestInstallationID, created.Server.ID, keyring.RecordStaticCredential)
	require.NoError(t, err)
	selected, _, err := f.coordinator.ReadActive(f.ctx, ns)
	require.NoError(t, err)
	generation, err := servercredentials.DecodeStaticGeneration(selected)
	require.NoError(t, err)
	require.Equal(t, "actual-static-canary", generation.Values["token"])
	clear(selected)
	require.True(t, f.adapter.Finish(nil))
	require.Contains(t, f.output.String(), "native retained delete failure")
	require.Contains(t, f.output.String(), "cleanup=unconfirmed")
	require.NotContains(t, f.output.String(), "actual-static-canary")
	require.NotContains(t, f.output.String(), "second-static-canary")
}
