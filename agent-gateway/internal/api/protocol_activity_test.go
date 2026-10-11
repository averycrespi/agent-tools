package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/stretchr/testify/require"
)

type protocolActivityFixture struct{ windows []string }

func (f *protocolActivityFixture) ProtocolActivity(_ context.Context, window string) (contract.ProtocolActivity, error) {
	f.windows = append(f.windows, window)
	return contract.ProtocolActivity{Window: window, Coverage: "unavailable"}, nil
}
func TestProtocolActivityReadBoundary(t *testing.T) {
	fixture := &protocolActivityFixture{}
	h := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, ProtocolActivity: fixture})
	handler, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: h.Authenticate, Next: h})
	require.NoError(t, err)
	headers := map[string]string{"Authorization": "Bearer " + testBearer}
	for _, query := range []string{"", "?window=15m", "?window=1h", "?window=24h"} {
		response := perform(handler, http.MethodGet, "/api/v2/protocol-activity"+query, "", headers)
		require.Equal(t, 200, response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		require.Contains(t, response.Body.String(), `"counts":null`)
	}
	require.Equal(t, []string{"1h", "15m", "1h", "24h"}, fixture.windows)
	for _, query := range []string{"?", "?window=", "?window=5m", "?window=1h&window=24h", "?limit=1", "?secret"} {
		require.Equal(t, 400, perform(handler, http.MethodGet, "/api/v2/protocol-activity"+query, "", headers).Code, query)
	}
	require.Equal(t, 401, perform(handler, http.MethodGet, "/api/v2/protocol-activity", "", nil).Code)
	require.Equal(t, 405, perform(handler, http.MethodPost, "/api/v2/protocol-activity", "", headers).Code)
	require.Equal(t, 400, perform(handler, http.MethodGet, "/api/v2/protocol-activity", "{}", headers).Code)
	require.Len(t, fixture.windows, 4)
}
