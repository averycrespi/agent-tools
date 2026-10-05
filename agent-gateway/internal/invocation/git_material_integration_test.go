//go:build integration

package invocation

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitAdmissionSelectedMaterialFence(t *testing.T) {
	for _, mode := range []string{"allow", "rotate", "edit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(audit.WithSystem(t.Context()), 5*time.Second)
			defer cancel()
			coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
			backend := &httpMemoryKeyring{values: map[string]string{}}
			provider, err := keyring.NewProviderWithBackend(invocationTestInstallationID, backend)
			require.NoError(t, err)
			materials, err := gitcredentials.NewService(audits.store, keyring.NewCoordinator(provider, audits.store, audits.clock, rand.Reader), authority, audits.clock, rand.Reader, invocationTestInstallationID)
			require.NoError(t, err)
			definition := contract.GitCredentialDefinition{Name: "Git", Origin: "https://example.com", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}
			created, err := materials.Create(ctx, definition, []byte("git-private-canary"))
			require.NoError(t, err)
			repo, err := authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "repo", URL: "https://example.com/repo", Aliases: []string{}, CredentialID: &created.ID})
			require.NoError(t, err)
			profile, err := authority.GetGitRoutingProfile(ctx)
			require.NoError(t, err)
			profile, err = authority.PutGitRoutingProfile(ctx, profile.Revision, []string{"https://example.com"})
			require.NoError(t, err)
			_, err = authority.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[]}`)})
			require.NoError(t, err)
			target, err := httppolicy.ParseRequest(repo.URL+"/info/refs?service=git-upload-pack", "GET", "example.com", "", nil)
			require.NoError(t, err)
			request, err := gitwire.New(ctx, target, repo, profile.Revision, http.Header{}, io.NopCloser(strings.NewReader("")))
			require.NoError(t, err)
			if mode == "rotate" {
				lease, err := authority.Authenticate(ctx, credential.Bearer)
				require.NoError(t, err)
				defer lease.Release()
				evaluation, err := authority.EvaluateGitAdmission(ctx, lease, "", "", request, publicHTTPFacts())
				require.NoError(t, err)
				require.NotNil(t, evaluation.Candidate)
				material, err := materials.Acquire(ctx, evaluation.Execution.Material.Credential)
				require.NoError(t, err)
				defer material.Clear()
				_, err = materials.Rotate(ctx, created.ID, created.Revision, []byte("rotated-private-canary"))
				require.NoError(t, err)
				require.Error(t, authority.ConfirmGit(ctx, evaluation.Candidate, "", request, materials))
				return
			}
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var blocked atomic.Bool
			backend.getBarrier = func() {
				if blocked.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
			}
			traffic := audits.traffic
			lease, err := authority.Authenticate(ctx, credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			results := make(chan GitAdmissionResult, 1)
			failures := make(chan error, 1)
			go func() {
				result, err := coordinator.AdmitGit(ctx, lease, identity, request, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, materials)
				results <- result
				failures <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("material acquisition did not enter")
			}
			if mode == "edit" {
				definition.Name = "Changed"
				_, err = materials.Update(ctx, created.ID, created.Revision, definition)
				require.NoError(t, err)
			}
			unblock()
			result, err := <-results, <-failures
			if mode == "allow" {
				require.NoError(t, err)
				require.True(t, result.DispatchAuthorized)
				headers, err := result.Material.Apply(repo.URL, nil)
				require.NoError(t, err)
				require.Equal(t, "Bearer git-private-canary", headers.Get("Authorization"))
				completion := gitTrafficCompletion()
				completion.Outcome = "nonmutation"
				result.Settle()
				require.NoError(t, coordinator.CompleteGit(t.Context(), result, completion))
			} else {
				require.Error(t, err)
				require.False(t, result.DispatchAuthorized)
				require.Nil(t, result.Material)
			}
			waitTraffic(t, traffic)
			history, err := traffic.GitHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			if mode == "allow" {
				require.NotNil(t, history.Records[0].Admission.Material)
				require.Equal(t, "1", history.Records[0].Admission.Material.Generation)
			} else {
				require.False(t, history.Records[0].Admission.Allowed)
			}
		})
	}
}
