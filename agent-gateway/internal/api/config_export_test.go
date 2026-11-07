package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/stretchr/testify/require"
)

type configFixture struct {
	calls int
	err   error
}

func (f *configFixture) ExportBundle(context.Context) ([]byte, error) {
	f.calls++
	return []byte("{\"format\":\"agent-gateway-config\",\"version\":2,\"resources\":[]}\n"), f.err
}

func TestConfigurationExportAdministratorReadBoundary(t *testing.T) {
	reader := new(configFixture)
	apiHandler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Configuration: reader})
	handler, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: apiHandler.Authenticate, Next: apiHandler})
	require.NoError(t, err)
	headers := map[string]string{"Authorization": "Bearer " + testBearer}
	for _, auth := range []map[string]string{nil, {"Authorization": "Bearer incorrect"}} {
		require.Equal(t, 401, perform(handler, http.MethodGet, contract.ConfigExportPath, "", auth).Code)
	}
	require.Equal(t, 403, perform(handler, http.MethodGet, contract.ConfigExportPath, "", map[string]string{"Authorization": "Bearer mgw_agent_not_admin"}).Code)
	require.Zero(t, reader.calls)
	for _, suffix := range []string{"?", "?limit=1", "?secret=true", "?x=1&x=2"} {
		require.Equal(t, 400, perform(handler, http.MethodGet, contract.ConfigExportPath+suffix, "", headers).Code)
	}
	require.Equal(t, 400, perform(handler, http.MethodGet, contract.ConfigExportPath, "{}", headers).Code)
	require.Equal(t, 405, perform(handler, http.MethodPost, contract.ConfigExportPath, "{}", headers).Code)
	require.Zero(t, reader.calls)
	response := perform(handler, http.MethodGet, contract.ConfigExportPath, "", headers)
	require.Equal(t, 200, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, "application/json", response.Header().Get("Content-Type"))
	require.Equal(t, "{\"format\":\"agent-gateway-config\",\"version\":2,\"resources\":[]}\n", response.Body.String())
	require.Equal(t, 1, reader.calls)
	reader.err = errors.New("private failure canary")
	response = perform(handler, http.MethodGet, contract.ConfigExportPath, "", headers)
	require.Equal(t, 503, response.Code)
	require.NotContains(t, response.Body.String(), "private failure canary")
	require.NotContains(t, response.Body.String(), "agent-gateway-config")
}
