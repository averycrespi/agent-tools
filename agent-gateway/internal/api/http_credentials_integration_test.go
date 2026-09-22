//go:build integration

package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type httpCredentialClock struct{}

func (httpCredentialClock) Now() time.Time { return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) }

type httpCredentialBackend struct {
	mu              sync.Mutex
	items           map[string]string
	probeErr        error
	setErr          error
	corruptManifest bool
}

func (b *httpCredentialBackend) Probe(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.probeErr
}
func (b *httpCredentialBackend) Set(service, user, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.setErr != nil {
		return b.setErr
	}
	if b.corruptManifest && strings.HasSuffix(user, ".manifest") {
		value = "malformed-manifest"
	}
	b.items[service+user] = value
	return nil
}
func (b *httpCredentialBackend) Get(service, user string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.items[service+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}
func (b *httpCredentialBackend) Delete(service, user string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, service+user)
	return nil
}

func newHTTPCredentialIntegrationHandler(t *testing.T) (http.Handler, *[]contract.Invalidation) {
	t.Helper()
	return newHTTPCredentialIntegrationHandlerWithBackend(t, &httpCredentialBackend{items: map[string]string{}})
}

func newHTTPCredentialIntegrationHandlerWithBackend(t *testing.T, backend *httpCredentialBackend) (http.Handler, *[]contract.Invalidation) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	store, err := storage.Initialize(t.Context(), owner, testID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	provider, err := keyring.NewProviderWithBackend(testID, backend)
	require.NoError(t, err)
	policies, err := authorization.New(store, httpCredentialClock{}, rand.Reader)
	require.NoError(t, err)
	repo, err := httpcredentials.NewRepository(store, httpCredentialClock{}, rand.Reader, policies)
	require.NoError(t, err)
	service, err := httpcredentials.NewService(repo, keyring.NewCoordinator(provider, store, httpCredentialClock{}, rand.Reader), testID)
	require.NoError(t, err)
	invalidations := []contract.Invalidation{}
	var invalidationMu sync.Mutex
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Principals: policies, HTTPPolicies: policies, HTTPCredentials: service, Invalidate: func(event contract.Invalidation) {
		invalidationMu.Lock()
		defer invalidationMu.Unlock()
		invalidations = append(invalidations, event)
	}})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary, &invalidations
}

func TestIntegrationHTTPCredentialKeyringFailureIsNotStorageFailure(t *testing.T) {
	for _, failure := range []string{"probe", "write", "readback"} {
		for _, operation := range []string{"create", "rotate"} {
			t.Run(failure+"/"+operation, func(t *testing.T) {
				backend := &httpCredentialBackend{items: map[string]string{}}
				handler, _ := newHTTPCredentialIntegrationHandlerWithBackend(t, backend)
				headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
				path, body := "/api/v2/http/credentials", httpCredentialCreateBody
				if operation == "rotate" {
					created := perform(handler, http.MethodPost, path, body, headers)
					require.Equal(t, http.StatusCreated, created.Code)
					var resource contract.HTTPCredential
					require.NoError(t, json.Unmarshal(created.Body.Bytes(), &resource))
					path += "/" + resource.ID + "/rotate"
					body = `{"secret":"rotation-canary"}`
					headers["If-Match"] = created.Header().Get("ETag")
				}
				backend.mu.Lock()
				switch failure {
				case "probe":
					backend.probeErr = errors.New("private-backend-canary")
				case "write":
					backend.setErr = errors.New("private-backend-canary")
				case "readback":
					backend.corruptManifest = true
				}
				backend.mu.Unlock()
				response := perform(handler, http.MethodPost, path, body, headers)
				require.Equal(t, http.StatusServiceUnavailable, response.Code)
				require.Contains(t, response.Body.String(), `"code":"keyring_unavailable"`)
				require.NotContains(t, response.Body.String(), "canary")
				listed := perform(handler, http.MethodGet, "/api/v2/http/credentials", "", map[string]string{"Authorization": "Bearer " + testBearer})
				var page contract.QueryCollection[contract.HTTPCredential]
				require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &page))
				require.Len(t, page.Items, 1)
				require.False(t, page.Items[0].Available)
				metadata, err := json.Marshal(page.Items[0].HTTPCredentialDefinition)
				require.NoError(t, err)
				headers["If-Match"] = contract.HTTPCredentialETag(page.Items[0].ID, page.Items[0].Revision)
				updated := perform(handler, http.MethodPatch, "/api/v2/http/credentials/"+page.Items[0].ID, string(metadata), headers)
				require.Equal(t, http.StatusOK, updated.Code, "keyring failure must not latch control storage")
			})
		}
	}
}

func TestIntegrationHTTPCredentialFailureClassification(t *testing.T) {
	capability := &keyring.CapabilityError{Capability: keyring.Capability{State: contract.KeyringLocked}}
	for _, materialError := range []error{capability, keyring.ErrWorkLimit, keyring.ErrNoAuthority, keyring.ErrNotFound, keyring.ErrCandidateLimit, keyring.ErrHandleCollision, keyring.ErrDraining, keyring.ErrSecretTooLarge, keyring.ErrIncompleteGeneration} {
		response := httptest.NewRecorder()
		writeHTTPCredentialError(response, materialError)
		require.Contains(t, response.Body.String(), `"code":"keyring_unavailable"`)
	}
	response := httptest.NewRecorder()
	writeHTTPCredentialError(response, errors.Join(capability, storage.ErrStorageLatched))
	require.Contains(t, response.Body.String(), `"code":"storage_unavailable"`, "a real storage latch must take precedence over a joined keyring error")
}

const httpCredentialCreateBody = `{"name":"API key","boundary":{"host":"api.example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"secret":"http-api-create-canary"}`

func TestIntegrationHTTPCredentialPublicLifecycle(t *testing.T) {
	handler, events := newHTTPCredentialIntegrationHandler(t)
	request := func(method, path, body, etag string) *httptest.ResponseRecorder {
		headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
		if etag != "" {
			headers["If-Match"] = etag
		}
		return perform(handler, method, path, body, headers)
	}
	root := "/api/v2/http/credentials"
	require.Equal(t, http.StatusUnauthorized, perform(handler, http.MethodGet, root, "", nil).Code)
	empty := request(http.MethodGet, root, "", "")
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	require.JSONEq(t, `{"items":[],"next_cursor":null,"total_count":0,"offset":0}`, empty.Body.String())
	created := request(http.MethodPost, root, httpCredentialCreateBody, "")
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var resource contract.HTTPCredential
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &resource))
	require.True(t, resource.Available)
	require.NotEmpty(t, resource.ID)
	require.Empty(t, resource.References)
	require.NotContains(t, created.Body.String(), "canary")
	require.Equal(t, "no-store", created.Header().Get("Cache-Control"))
	path := root + "/" + resource.ID
	etag := created.Header().Get("ETag")
	require.Equal(t, contract.HTTPCredentialETag(resource.ID, resource.Revision), etag)
	get := request(http.MethodGet, path, "", "")
	require.Equal(t, http.StatusOK, get.Code, get.Body.String())
	require.Equal(t, etag, get.Header().Get("ETag"))
	list := request(http.MethodGet, root+"?limit=1", "", "")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var page contract.QueryCollection[contract.HTTPCredential]
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, 1, page.TotalCount)
	require.Equal(t, http.StatusPreconditionRequired, request(http.MethodPost, path+"/rotate", `{"secret":"next"}`, "").Code)
	rotate := request(http.MethodPost, path+"/rotate", `{"secret":"http-api-rotation-canary"}`, etag)
	require.Equal(t, http.StatusOK, rotate.Code, rotate.Body.String())
	require.NotContains(t, rotate.Body.String(), "canary")
	require.NotEqual(t, etag, rotate.Header().Get("ETag"))
	require.Equal(t, http.StatusPreconditionFailed, request(http.MethodDelete, path, `{}`, etag).Code)
	etag = rotate.Header().Get("ETag")
	patch := request(http.MethodPatch, path, `{"name":"Renamed","boundary":{"host":"api.example.com","port":443,"allow_wildcard":false},"recipe":{"header":"X-API-Key","prefix":""}}`, etag)
	require.Equal(t, http.StatusOK, patch.Code, patch.Body.String())
	require.Contains(t, patch.Body.String(), `"name":"Renamed"`)
	require.Equal(t, http.StatusNoContent, request(http.MethodDelete, path, `{}`, patch.Header().Get("ETag")).Code)
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, path, "", "").Code)
	require.Len(t, *events, 8)
	for index, event := range *events {
		expected := contract.InvalidationHTTPCredentials
		if index%2 == 1 {
			expected = contract.InvalidationSystemStatus
		}
		require.Equal(t, expected, event.Kind)
		encoded, err := json.Marshal(event)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "canary")
	}
}

func TestIntegrationHTTPCredentialStrictIngress(t *testing.T) {
	handler, _ := newHTTPCredentialIntegrationHandler(t)
	root := "/api/v2/http/credentials"
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	for _, body := range []string{
		strings.Replace(httpCredentialCreateBody, `"name":"API key"`, `"name":"API key","name":"duplicate"`, 1),
		strings.Replace(httpCredentialCreateBody, `"secret":`, `"unknown":1,"secret":`, 1),
		strings.Replace(httpCredentialCreateBody, `"Authorization"`, `"Host"`, 1),
		strings.Replace(httpCredentialCreateBody, `"http-api-create-canary"`, `"canary\r\nInjected: yes"`, 1),
		strings.Replace(httpCredentialCreateBody, `"http-api-create-canary"`, `null`, 1),
		strings.Replace(httpCredentialCreateBody, `"api.example.com"`, `"*.example.com"`, 1),
		strings.Replace(httpCredentialCreateBody, `"http-api-create-canary"`, `"`+strings.Repeat("x", 4097)+`"`, 1),
	} {
		response := perform(handler, http.MethodPost, root, body, headers)
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.NotContains(t, response.Body.String(), "canary")
	}
	for _, query := range []string{"?unknown=1", "?limit=1&limit=2", "?cursor=canary", "?limit=0"} {
		response := perform(handler, http.MethodGet, root+query, "", headers)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	require.Equal(t, http.StatusMethodNotAllowed, perform(handler, http.MethodPut, root, httpCredentialCreateBody, headers).Code)
	response := perform(handler, http.MethodGet, root, "", headers)
	require.JSONEq(t, `{"items":[],"next_cursor":null,"total_count":0,"offset":0}`, response.Body.String())
}
