package oauth

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestRefreshDiagnosticsKeepSuccessDebugAndReauthorizationActionable(t *testing.T) {
	for _, level := range []diagnostics.Level{diagnostics.Warn, diagnostics.Info, diagnostics.Debug} {
		for _, success := range []bool{false, true} {
			operation := &refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthNone))}
			requester := &refreshRequesterFake{status: http.StatusOK, header: http.Header{"Content-Type": {contract.MediaTypeJSON}}, body: []byte(`{"access_token":"PRIVATE-new-access","token_type":"Bearer","refresh_token":"PRIVATE-new-refresh"}`)}
			if !success {
				requester.status, requester.body = http.StatusBadRequest, []byte(`{"error":"invalid_grant","error_description":"PRIVATE-provider-response-canary"}`)
			}
			service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthNone)}, &refreshCoordinatorFake{operation: operation}, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, nil, nil)
			var output bytes.Buffer
			adapter := diagnostics.New(&output, level)
			t.Cleanup(func() { adapter.Finish(nil) })
			service.SetDiagnostics(adapter, func(string) uint64 { return 4 })
			result, err := service.Refresh(t.Context(), RefreshRequest{ServerID: refreshServerID})
			if success {
				require.NoError(t, err)
				require.True(t, result.Refreshed)
				require.NotEmpty(t, operation.installed)
			} else {
				require.ErrorIs(t, err, ErrRefreshReauthorization)
				require.Empty(t, operation.installed)
			}
			require.Equal(t, 1, requester.calls)
			service.Shutdown()
			require.True(t, adapter.Finish(nil))
			switch {
			case success && level != diagnostics.Debug:
				require.Empty(t, output.String())
			case success:
				require.Contains(t, output.String(), `"event":"oauth_refresh_complete"`)
				require.Contains(t, output.String(), `"level":"DEBUG"`)
			default:
				require.Contains(t, output.String(), `"event":"oauth_refresh_failed"`)
				require.Contains(t, output.String(), `"level":"WARN"`)
				require.Contains(t, output.String(), `"disposition":"operator_authentication_required"`)
			}
			for _, canary := range []string{"PRIVATE-new-access", "PRIVATE-new-refresh", "PRIVATE-provider-response-canary", "old-access", "old-refresh", refreshServerID, "https://"} {
				require.NotContains(t, output.String(), canary)
			}
		}
	}
}
