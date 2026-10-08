//go:build integration

package httpproxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitResolutionPreservesNativeCause(t *testing.T) {
	f := fixture(t)
	ctx := audit.WithSystem(t.Context())
	_, err := f.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Inventory", URL: "https://private-host-canary.example/repo", Aliases: []string{}})
	require.NoError(t, err)
	profile, err := f.authority.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	_, err = f.authority.PutGitRoutingProfile(ctx, profile.Revision, []string{"https://private-host-canary.example"})
	require.NoError(t, err)
	f.engine.options.Remote = remote.New(remote.Options{Resolver: failedDiagnosticResolver{}})
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	f.engine.options.Diagnostics = adapter
	t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
	lease, err := f.authority.Authenticate(t.Context(), f.credential.Bearer)
	require.NoError(t, err)
	destination, err := httppolicy.NewDestination("private-host-canary.example", 443)
	require.NoError(t, err)
	inside := &intercepted{destination: destination, bearer: f.credential.Bearer, binding: lease.Binding(), sni: destination.Host()}
	lease.Release()
	request := httptest.NewRequest(http.MethodGet, "http://private-host-canary.example/repo/info/refs?service=git-upload-pack", nil)
	request.RequestURI = "/repo/info/refs?service=git-upload-pack"
	request.URL.Scheme, request.URL.Host = "", ""
	request.Header.Set("Authorization", "Bearer private-upstream-token-canary")
	response := httptest.NewRecorder()
	f.engine.handle(response, request, inside)
	require.Equal(t, http.StatusBadGateway, response.Code)
	require.NotContains(t, response.Body.String(), "lookup failed")
	require.True(t, adapter.Finish(nil))
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record), "one observation owns the failure")
	require.Equal(t, "http_proxy_rejected", record["event"])
	require.Contains(t, output.String(), "lookup failed: no such host")
	require.Contains(t, output.String(), "private-host-canary.example")
	require.NotContains(t, output.String(), "private-upstream-token-canary")
	require.NotContains(t, output.String(), f.credential.Bearer)
}
