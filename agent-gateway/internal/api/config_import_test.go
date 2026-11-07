package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/configdefinition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configimport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/stretchr/testify/require"
)

type configurationInputFixture struct {
	previews, imports int
	err               error
}

func (f *configurationInputFixture) Preview(context.Context, []byte) (contract.ConfigPreview, error) {
	f.previews++
	return contract.ConfigPreview{Advisory: true, Creations: []contract.ConfigCreation{}}, f.err
}
func (f *configurationInputFixture) Import(context.Context, []byte) (contract.ConfigImportResult, error) {
	f.imports++
	return contract.ConfigImportResult{Configuration: "committed", Creations: []contract.ConfigCreated{}, Activation: "pending"}, f.err
}
func TestConfigurationInputAdministratorBoundary(t *testing.T) {
	service := new(configurationInputFixture)
	apiHandler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, ConfigurationImport: service})
	handler, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: apiHandler.Authenticate, Next: apiHandler})
	require.NoError(t, err)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": "application/json"}
	for _, path := range []string{contract.ConfigPreviewPath, contract.ConfigImportPath} {
		require.Equal(t, 401, perform(handler, http.MethodPost, path, `{}`, nil).Code)
		require.Equal(t, 403, perform(handler, http.MethodPost, path, `{}`, map[string]string{"Authorization": "Bearer mgw_agent_not_admin", "Content-Type": "application/json"}).Code)
		require.Equal(t, 405, perform(handler, http.MethodGet, path, "", headers).Code)
		require.Equal(t, 400, perform(handler, http.MethodPost, path+"?", `{}`, headers).Code)
		require.Equal(t, 400, perform(handler, http.MethodPost, path+"?mode=skip-identical", `{}`, headers).Code)
	}
	require.Zero(t, service.previews)
	require.Zero(t, service.imports)
	r := perform(handler, http.MethodPost, contract.ConfigPreviewPath, `{}`, headers)
	require.Equal(t, 200, r.Code)
	require.Equal(t, "no-store", r.Header().Get("Cache-Control"))
	require.Zero(t, service.imports)
	r = perform(handler, http.MethodPost, contract.ConfigImportPath, `{}`, headers)
	require.Equal(t, 201, r.Code)
	require.Contains(t, r.Body.String(), `"configuration":"committed"`)
	for _, test := range []struct {
		err    error
		status int
	}{{configdefinition.ErrInvalid, 400}, {configimport.ErrConflict, 409}, {errors.New("secret-payload-canary"), 503}} {
		service.err = test.err
		r = perform(handler, http.MethodPost, contract.ConfigImportPath, `{}`, headers)
		require.Equal(t, test.status, r.Code)
		require.NotContains(t, r.Body.String(), "secret-payload-canary")
	}
}
