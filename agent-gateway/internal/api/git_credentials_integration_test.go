//go:build integration

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type gitFailingSetBackend struct {
	*httpCredentialBackend
	fail   atomic.Bool
	failed atomic.Bool
}

func (b *gitFailingSetBackend) observe(point string) error {
	if point == "before_write" && b.fail.Load() {
		b.failed.Store(true)
		return errors.New("provider-private-failure")
	}
	return b.httpCredentialBackend.observe(point)
}

func TestIntegrationGitCredentialProviderFailureAndLostAuditAck(t *testing.T) {
	backend := &gitFailingSetBackend{httpCredentialBackend: &httpCredentialBackend{items: map[string]string{}}}
	var faults atomic.Int32
	h, store := newGitIntegrationHandlerWithFault(t, backend, func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit && backend.failed.Load() {
			faults.Add(1)
			return errors.New("audit-private-lost-ack")
		}
		return nil
	})
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	created := perform(h, http.MethodPost, "/api/v2/git/credentials", `{"name":"Git","origin":"https://example.com","recipe":{"header":"Authorization","prefix":"Bearer "},"secret":"private-old"}`, headers)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var c contract.GitCredential
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))
	headers["If-Match"] = created.Header().Get("ETag")
	backend.fail.Store(true)
	response := perform(h, http.MethodPost, "/api/v2/git/credentials/"+c.ID+"/rotate", `{"secret":"private-new"}`, headers)
	require.True(t, backend.failed.Load())
	require.Positive(t, faults.Load())
	require.True(t, store.Latched())
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"code":"storage_unavailable"`)
	require.NotContains(t, response.Body.String(), "keyring_unavailable")
	require.NotContains(t, response.Body.String(), "private")
}

func TestIntegrationGitCredentialDeleteRequiresEmptyObject(t *testing.T) {
	h := newGitIntegrationHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	created := perform(h, http.MethodPost, "/api/v2/git/credentials", `{"name":"Git","origin":"https://example.com","recipe":{"header":"Authorization","prefix":"Bearer "},"secret":"private"}`, headers)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var c contract.GitCredential
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))
	headers["If-Match"] = created.Header().Get("ETag")
	path := "/api/v2/git/credentials/" + c.ID
	for _, body := range []string{"", "null", "[]", `{"extra":true}`} {
		require.Equal(t, http.StatusBadRequest, perform(h, http.MethodDelete, path, body, headers).Code, body)
	}
	deleted := perform(h, http.MethodDelete, path, `{}`, headers)
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())
	require.Empty(t, deleted.Body.String())
	require.Equal(t, http.StatusNotFound, perform(h, http.MethodGet, path, "", headers).Code)
}

func TestIntegrationGitCredentialSecureIngressPreconditionsAndReferences(t *testing.T) {
	h := newGitIntegrationHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	body := `{"name":"Git access","origin":"https://example.com","recipe":{"header":"Authorization","prefix":"Bearer "},"secret":"private-git-api-canary"}`
	created := perform(h, http.MethodPost, "/api/v2/git/credentials", body, headers)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.NotContains(t, created.Body.String(), "private-git-api-canary")
	var c contract.GitCredential
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))
	require.True(t, c.Available)
	path := "/api/v2/git/credentials/" + c.ID
	require.Equal(t, 428, perform(h, http.MethodPost, path+"/rotate", `{"secret":"second-private-canary"}`, headers).Code)
	headers["If-Match"] = created.Header().Get("ETag")
	for _, invalid := range []string{`{"secret":"value","fallback":true}`, `{"secret":{"encoding":"store","path":"private"}}`, `{"secret":"value\r\nInjected: bad"}`} {
		response := perform(h, http.MethodPost, path+"/rotate", invalid, headers)
		require.Equal(t, 400, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), "Injected")
	}
	rotated := perform(h, http.MethodPost, path+"/rotate", `{"secret":"second-private-canary"}`, headers)
	require.Equal(t, 200, rotated.Code, rotated.Body.String())
	require.NotContains(t, rotated.Body.String(), "private-canary")
	require.Equal(t, 412, perform(h, http.MethodDelete, path, `{}`, headers).Code)
	delete(headers, "If-Match")
	read := perform(h, http.MethodGet, path, "", headers)
	require.Equal(t, 200, read.Code)
	require.Equal(t, "no-store", read.Header().Get("Cache-Control"))
	list := perform(h, http.MethodGet, "/api/v2/git/credentials", "", headers)
	require.Equal(t, 200, list.Code, list.Body.String())
	require.NotContains(t, list.Body.String(), "private-canary")
	repoBody := fmt.Sprintf(`{"name":"Repository","url":"https://example.com/team/repo","aliases":[],"credential_id":%q}`, c.ID)
	repo := perform(h, http.MethodPost, "/api/v2/git/repositories", repoBody, headers)
	require.Equal(t, 201, repo.Code, repo.Body.String())
	headers["If-Match"] = read.Header().Get("ETag")
	require.Equal(t, 409, perform(h, http.MethodDelete, path, `{}`, headers).Code)
	retarget := `{"name":"Git access","origin":"https://evil.example","recipe":{"header":"Authorization","prefix":"Bearer "}}`
	require.Equal(t, 409, perform(h, http.MethodPatch, path, retarget, headers).Code)
	require.Equal(t, 404, perform(h, http.MethodGet, "/api/v2/http/credentials/"+c.ID, "", headers).Code)
	delete(headers, "Authorization")
	require.Equal(t, 401, perform(h, http.MethodGet, path, "", headers).Code)
}
