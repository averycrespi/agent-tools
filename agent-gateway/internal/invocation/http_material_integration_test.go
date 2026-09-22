//go:build integration

package invocation

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type httpMemoryKeyring struct {
	mu     sync.Mutex
	values map[string]string
}

func (*httpMemoryKeyring) Probe(context.Context, string) error { return nil }
func (m *httpMemoryKeyring) Set(service, user, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[service+user] = value
	return nil
}
func (m *httpMemoryKeyring) Get(service, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[service+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (m *httpMemoryKeyring) Delete(service, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, service+user)
	return nil
}

func TestIntegrationHTTPAdmissionSealsSelectedMaterialGeneration(t *testing.T) {
	for _, mode := range []string{"allow", "rotate", "edit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(audit.WithSystem(t.Context()), 5*time.Second)
			defer cancel()
			coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
			repository, err := httpcredentials.NewRepository(audits.store, audits.clock, rand.Reader, authority)
			require.NoError(t, err)
			provider, err := keyring.NewProviderWithBackend(invocationTestInstallationID, &httpMemoryKeyring{values: map[string]string{}})
			require.NoError(t, err)
			materials, err := httpcredentials.NewService(repository, keyring.NewCoordinator(provider, audits.store, audits.clock, rand.Reader), invocationTestInstallationID)
			require.NoError(t, err)
			definition := httpcredentials.Definition{Name: "Injection", Boundary: httpcredentials.Boundary{Host: "example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}
			created, err := materials.Create(ctx, definition, []byte("injected-private-canary"))
			require.NoError(t, err)
			policy := json.RawMessage(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}},"credential_id":"` + created.ID + `"}`)
			_, err = authority.PutHTTPGrant(ctx, "", "", authorization.HTTPGrantInput{PrincipalID: principal.ID, Policy: policy})
			require.NoError(t, err)
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var once sync.Once
			traffic, _ := trafficFixture(t, nil, func(point string) error {
				if point == "acknowledgment" {
					once.Do(func() {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
				return nil
			})
			audits.traffic = traffic
			lease, err := authority.Authenticate(ctx, credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			results := make(chan HTTPAdmissionResult, 1)
			failures := make(chan error, 1)
			go func() {
				result, err := coordinator.AdmitHTTP(ctx, lease, identity, authorization.HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com/private?token=observed-private-canary", Method: "GET"}, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, materials)
				results <- result
				failures <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("admission did not commit")
			}
			switch mode {
			case "rotate":
				_, err = materials.Rotate(ctx, created.ID, created.Revision, []byte("new-private-canary"))
				require.NoError(t, err)
			case "edit":
				definition.Name = "Renamed injection"
				_, err = materials.Update(ctx, created.ID, created.Revision, definition)
				require.NoError(t, err)
			}
			unblock()
			result, admissionErr := <-results, <-failures
			require.True(t, result.Committed)
			assert.Equal(t, mode == "allow", result.DispatchAuthorized)
			if mode == "allow" {
				require.NoError(t, admissionErr)
				require.NotNil(t, result.Material)
				target, parseErr := httppolicy.ParseRequest("https://example.com/", "GET", "example.com", "", nil)
				require.NoError(t, parseErr)
				headers, headerErr := result.Material.Headers(target, nil)
				require.NoError(t, headerErr)
				assert.Equal(t, "Bearer injected-private-canary", headers.Get("Authorization"))
				require.NoError(t, coordinator.CompleteHTTP(ctx, result, httpTrafficCompletion()))
			} else {
				require.Error(t, admissionErr)
				assert.Nil(t, result.Material)
			}
			history, err := traffic.HTTPHistory(ctx, 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			require.NotNil(t, history.Records[0].Admission.Material)
			assert.Equal(t, "1", history.Records[0].Admission.Material.Generation)
			encoded, err := json.Marshal(history)
			require.NoError(t, err)
			for _, canary := range []string{"injected-private-canary", "observed-private-canary", "new-private-canary"} {
				assert.NotContains(t, string(encoded), canary)
			}
		})
	}
}
