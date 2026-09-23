//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationPrincipalCreationHTTPDefault(t *testing.T) {
	h, _ := newHTTPCredentialIntegrationHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	for _, policy := range []string{"block", "allow"} {
		created := perform(h, http.MethodPost, "/api/v2/principals", `{"display_name":"Agent","visibility":"requestable","http_default":"`+policy+`"}`, headers)
		require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
		var result contract.PrincipalCreation
		require.NoError(t, json.Unmarshal(created.Body.Bytes(), &result))
		require.EqualValues(t, policy, result.Principal.HTTPDefault)
		require.Equal(t, "1", result.Principal.Revision)
		read := perform(h, http.MethodGet, "/api/v2/principals/"+result.Principal.ID, "", headers)
		var actual contract.Principal
		require.NoError(t, json.Unmarshal(read.Body.Bytes(), &actual))
		require.Equal(t, result.Principal, actual)
	}
	for _, value := range []string{`null`, `true`, `1`, `""`, `"unknown"`} {
		failed := perform(h, http.MethodPost, "/api/v2/principals", `{"display_name":"Invalid","visibility":"requestable","http_default":`+value+`}`, headers)
		require.Equal(t, http.StatusBadRequest, failed.Code, failed.Body.String())
	}
}

func TestIntegrationPrincipalSettingsStrictAtomicContract(t *testing.T) {
	h, _ := newHTTPCredentialIntegrationHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	created := perform(h, http.MethodPost, "/api/v2/principals", `{"display_name":"Original","visibility":"requestable"}`, headers)
	require.Equal(t, http.StatusCreated, created.Code)
	var p contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &p))
	require.Equal(t, contract.HTTPDefaultBlock, p.Principal.HTTPDefault)
	path := "/api/v2/principals/" + p.Principal.ID
	missing := perform(h, http.MethodPatch, path, `{"http_default":"allow"}`, headers)
	require.Equal(t, http.StatusPreconditionRequired, missing.Code)
	for _, body := range []string{
		`{"http_default":null}`, `{"http_default":true}`, `{"http_default":1}`, `{"http_default":"unknown","display_name":"Unsaved"}`,
		`{"http_default":"allow","http_default":"block"}`, `{"default":"allow"}`, `{"http_default":"allow","extra":true}`,
	} {
		headers["If-Match"] = created.Header().Get("ETag")
		failed := perform(h, http.MethodPatch, path, body, headers)
		require.Equal(t, http.StatusBadRequest, failed.Code, failed.Body.String())
		unchanged := perform(h, http.MethodGet, path, "", headers)
		require.Equal(t, created.Header().Get("ETag"), unchanged.Header().Get("ETag"))
		var actual contract.Principal
		require.NoError(t, json.Unmarshal(unchanged.Body.Bytes(), &actual))
		require.Equal(t, p.Principal, actual)
	}
	headers["If-Match"] = `"http-default-` + p.Principal.ID + `-1"`
	require.Equal(t, http.StatusPreconditionFailed, perform(h, http.MethodPatch, path, `{"http_default":"allow"}`, headers).Code)
	headers["If-Match"] = created.Header().Get("ETag")
	changed := perform(h, http.MethodPatch, path, `{"display_name":"Combined","state":"disabled","visibility":"all","http_default":"allow"}`, headers)
	require.Equal(t, http.StatusOK, changed.Code, changed.Body.String())
	var actual contract.Principal
	require.NoError(t, json.Unmarshal(changed.Body.Bytes(), &actual))
	require.Equal(t, "2", actual.Revision)
	require.Equal(t, "Combined", actual.DisplayName)
	require.Equal(t, contract.PrincipalDisabled, actual.State)
	require.Equal(t, contract.VisibilityAll, actual.Visibility)
	require.Equal(t, contract.HTTPDefaultAllow, actual.HTTPDefault)
	require.Equal(t, contract.PrincipalETag(actual.ID, actual.Revision), changed.Header().Get("ETag"))
}

func TestIntegrationPrincipalSettingsConcurrentWriters(t *testing.T) {
	h, _ := newHTTPCredentialIntegrationHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	created := perform(h, http.MethodPost, "/api/v2/principals", `{"display_name":"Original","visibility":"requestable"}`, headers)
	require.Equal(t, http.StatusCreated, created.Code)
	var p contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &p))
	path := "/api/v2/principals/" + p.Principal.ID
	headers["If-Match"] = created.Header().Get("ETag")
	start := make(chan struct{})
	identity, policy := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { <-start; identity <- perform(h, http.MethodPatch, path, `{"display_name":"Winner"}`, headers) }()
	go func() { <-start; policy <- perform(h, http.MethodPatch, path, `{"http_default":"allow"}`, headers) }()
	close(start)
	a, b := <-identity, <-policy
	require.True(t, a.Code == http.StatusOK || b.Code == http.StatusOK)
	require.False(t, a.Code == http.StatusOK && b.Code == http.StatusOK, "one precondition cannot authorize two writes")
	failed := a
	if a.Code == http.StatusOK {
		failed = b
	}
	require.Contains(t, []int{http.StatusPreconditionFailed, http.StatusTooManyRequests}, failed.Code, failed.Body.String())
	read := perform(h, http.MethodGet, path, "", headers)
	var actual contract.Principal
	require.NoError(t, json.Unmarshal(read.Body.Bytes(), &actual))
	require.Equal(t, "2", actual.Revision)
	if a.Code == http.StatusOK {
		require.Equal(t, "Winner", actual.DisplayName)
		require.Equal(t, contract.HTTPDefaultBlock, actual.HTTPDefault)
	} else {
		require.Equal(t, "Original", actual.DisplayName)
		require.Equal(t, contract.HTTPDefaultAllow, actual.HTTPDefault)
	}
}
