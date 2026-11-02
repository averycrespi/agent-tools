//go:build integration

package invocation

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestIntegrationUnsetHTTPBindingRefusesDispatchWithoutFallback(t *testing.T) {
	ctx := audit.WithSystem(t.Context())
	coordinator, audits, authority, principal, credential, _ := newAdmissionCoordinatorWithOwnership(t, nil)
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	require.NoError(t, audits.store.Mutate(ctx, func(tx *sql.Tx) error {
		return httpcredentials.StageImportedDefinitionTx(ctx, tx, id, httpcredentials.Definition{Name: "Imported", Boundary: httpcredentials.Boundary{Host: "example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, audits.clock.Now())
	}))
	_, err := authority.PutHTTPGrant(ctx, "", "", authorization.HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}},"credential_id":"` + id + `"}`)})
	require.NoError(t, err)
	// An overlapping ordinary allow cannot turn the required unset binding into public access.
	_, err = authority.PutHTTPGrant(ctx, "", "", authorization.HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}}}`)})
	require.NoError(t, err)
	_, err = authority.PutHTTPGrant(ctx, "", "", authorization.HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"other.example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}}}`)})
	require.NoError(t, err)
	lease, err := authority.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	for _, host := range []string{"example.com", "other.example.com"} {
		identity, err := audits.PrepareIdentity()
		require.NoError(t, err)
		result, err := coordinator.AdmitHTTP(ctx, lease, identity, authorization.HTTPAccessInput{PrincipalID: principal.ID, URL: "https://" + host + "/", Method: "GET"}, publicHTTPFacts(), nil)
		require.NoError(t, err)
		if host == "example.com" {
			require.False(t, result.DispatchAuthorized)
			require.Equal(t, contract.HTTPReasonCredentialUnavailable, result.Execution.Reason)
			require.Nil(t, result.Material)
		} else {
			require.True(t, result.DispatchAuthorized)
			result.Settle()
		}
	}
}

func TestIntegrationUnsetGitBindingRefusesDispatchWithoutFallback(t *testing.T) {
	ctx := audit.WithSystem(t.Context())
	coordinator, audits, authority, principal, credential, _ := newAdmissionCoordinatorWithOwnership(t, nil)
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	require.NoError(t, audits.store.Mutate(ctx, func(tx *sql.Tx) error {
		return gitcredentials.StageImportedDefinitionTx(ctx, tx, id, contract.GitCredentialDefinition{Name: "Imported", Origin: "https://example.com", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, audits.clock.Now())
	}))
	profile, err := authority.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	profile, err = authority.PutGitRoutingProfile(ctx, profile.Revision, []string{"https://example.com"})
	require.NoError(t, err)
	lease, err := authority.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	for _, bound := range []bool{true, false} {
		name := "public"
		var credentialID *string
		if bound {
			name = "protected"
			credentialID = &id
		}
		repo, err := authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: name, URL: "https://example.com/" + name, Aliases: []string{}, CredentialID: credentialID})
		require.NoError(t, err)
		_, err = authority.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[]}`)})
		require.NoError(t, err)
		target, err := httppolicy.ParseRequest(repo.URL+"/info/refs?service=git-upload-pack", "GET", "example.com", "", nil)
		require.NoError(t, err)
		request, err := gitwire.New(ctx, target, repo, profile.Revision, http.Header{}, io.NopCloser(strings.NewReader("")))
		require.NoError(t, err)
		identity, err := audits.PrepareIdentity()
		require.NoError(t, err)
		result, err := coordinator.AdmitGit(ctx, lease, identity, request, publicHTTPFacts(), nil)
		require.NoError(t, err)
		require.Equal(t, !bound, result.DispatchAuthorized)
		if bound {
			require.True(t, result.Execution.CredentialUnavailable)
			require.Nil(t, result.Material)
		} else {
			result.Settle()
		}
	}
}
