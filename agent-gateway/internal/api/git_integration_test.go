//go:build integration

package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

var gitAPITemplate = storagefixture.New(testID)

func newGitIntegrationHandler(t *testing.T) http.Handler {
	t.Helper()
	h, _ := newGitIntegrationHandlerWithFault(t, &httpCredentialBackend{items: map[string]string{}}, nil)
	return h
}

func newGitIntegrationHandlerWithFault(t *testing.T, backend keyring.Backend, fault func(storage.FaultPoint) error) (http.Handler, *storage.Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	var store *storage.Store
	if fault == nil {
		store, err = gitAPITemplate.Open(t.Context(), owner)
	} else {
		store, err = storage.InitializeWithFaultInjection(t.Context(), owner, testID, fault)
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	policies, err := authorization.New(store, httpCredentialClock{}, rand.Reader)
	require.NoError(t, err)
	provider, err := keyring.NewProviderWithBackend(testID, backend)
	require.NoError(t, err)
	secrets, err := gitcredentials.NewService(store, keyring.NewCoordinator(provider, store, httpCredentialClock{}, rand.Reader), policies, httpCredentialClock{}, rand.Reader, testID)
	require.NoError(t, err)
	h := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Principals: policies, GitPolicies: policies, GitCredentials: secrets})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: h.Authenticate, Next: h})
	require.NoError(t, err)
	return boundary, store
}

func TestIntegrationGitRepositoryGrantAndProfileContracts(t *testing.T) {
	h := newGitIntegrationHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	principal := perform(h, http.MethodPost, "/api/v2/principals", `{"display_name":"Git Agent","visibility":"all"}`, headers)
	require.Equal(t, 201, principal.Code)
	var p contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(principal.Body.Bytes(), &p))
	body := `{"name":"Repository","url":"https://example.com/team/repo","aliases":[],"credential_id":null}`
	created := perform(h, http.MethodPost, "/api/v2/git/repositories", body, headers)
	require.Equal(t, 201, created.Code, created.Body.String())
	require.Equal(t, "no-store", created.Header().Get("Cache-Control"))
	var repo contract.GitRepository
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &repo))
	path := "/api/v2/git/repositories/" + repo.ID
	require.Equal(t, 428, perform(h, http.MethodPatch, path, body, headers).Code)
	headers["If-Match"] = created.Header().Get("ETag")
	retarget := `{"name":"Repository","url":"https://evil.example/team/repo","aliases":[],"credential_id":null}`
	require.Equal(t, 409, perform(h, http.MethodPatch, path, retarget, headers).Code)
	edited := perform(h, http.MethodPatch, path, body, headers)
	require.Equal(t, 200, edited.Code, edited.Body.String())
	require.Equal(t, 412, perform(h, http.MethodDelete, path, "", headers).Code)
	delete(headers, "If-Match")
	grantBody := fmt.Sprintf(`{"principal_id":%q,"repository_id":%q,"description":null,"policy":{"version":1,"read":true,"refs":[]},"expires_at":null}`, p.Principal.ID, repo.ID)
	grant := perform(h, http.MethodPost, "/api/v2/git/grants", grantBody, headers)
	require.Equal(t, 201, grant.Code, grant.Body.String())
	badWrite := fmt.Sprintf(`{"principal_id":%q,"repository_id":%q,"description":null,"policy":{"version":1,"read":false,"refs":[{"ref":{"kind":"exact","value":"refs/heads/main"},"actions":["update"]}]},"expires_at":null}`, p.Principal.ID, repo.ID)
	require.Equal(t, 400, perform(h, http.MethodPost, "/api/v2/git/grants", badWrite, headers).Code)
	profile := perform(h, http.MethodGet, "/api/v2/git/routing-profile", "", headers)
	require.Equal(t, 200, profile.Code)
	headers["If-Match"] = profile.Header().Get("ETag")
	updatedProfile := perform(h, http.MethodPatch, "/api/v2/git/routing-profile", `{"origins":["https://example.com"]}`, headers)
	require.Equal(t, 200, updatedProfile.Code, updatedProfile.Body.String())
	require.Contains(t, updatedProfile.Body.String(), `"active":false`)
	headers["If-Match"] = edited.Header().Get("ETag")
	require.Equal(t, 204, perform(h, http.MethodDelete, path, "", headers).Code)
	delete(headers, "If-Match")
	retained := perform(h, http.MethodGet, "/api/v2/git/routing-profile", "", headers)
	require.JSONEq(t, updatedProfile.Body.String(), retained.Body.String())
	for _, bad := range []string{"?limit=0", "?limit=01", "?limit=101", "?limit=1&limit=2", "?principal_id=" + p.Principal.ID} {
		require.Equal(t, 400, perform(h, http.MethodGet, "/api/v2/git/grants"+bad, "", headers).Code)
	}
	page := perform(h, http.MethodGet, "/api/v2/git/grants?limit=1", "", headers)
	require.Equal(t, 200, page.Code, page.Body.String())
	require.Contains(t, page.Body.String(), `"total_count":1`)
	unauthenticated := perform(h, http.MethodGet, "/api/v2/git/grants", "", nil)
	require.Equal(t, 401, unauthenticated.Code)
	malformed := perform(h, http.MethodPost, "/api/v2/git/repositories", `{"name":"A","url":"https://example.com/a","aliases":[],"credential_id":null,"http_default":"allow"}`, headers)
	require.Equal(t, 400, malformed.Code)
}
