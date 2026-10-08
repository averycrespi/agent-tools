package oauth

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
)

func TestOAuthNetworkSourcesRetainNativeCause(t *testing.T) {
	const secret = "actual-oauth-header-value"
	factory := remote.New(remote.Options{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("TLS connection refused " + secret)
	}})
	requester := hardenedRequester{factory: factory}
	_, _, _, err := requester.Request(t.Context(), "https://127.0.0.1:443/register", true, http.MethodPost, http.Header{"Authorization": {"Bearer " + secret}}, []byte(`{}`), 1024)
	require.ErrorIs(t, err, ErrRegistrationRejected)
	detail := diagnostics.Snapshot("oauth", "register", "fixture", err)
	require.Contains(t, detail.Explanation, "TLS connection refused")
	require.NotContains(t, detail.Explanation, secret)
	metadata := remoteFetcher{factory: remote.New(remote.Options{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("metadata TLS certificate expired")
	}})}
	_, _, _, err = metadata.Fetch(t.Context(), "https://127.0.0.1:443/metadata", true)
	require.ErrorIs(t, err, ErrTrustRejected)
	require.Contains(t, diagnostics.Snapshot("oauth", "discover", "fixture", err).Explanation, "metadata TLS certificate expired")
}

type failingRefreshDiscovery struct{}

func (failingRefreshDiscovery) Discover(context.Context, Input) (Graph, error) {
	return Graph{}, errors.New("lookup issuer.example: no such host old-access old-refresh")
}
func TestRefreshDiscoveryCauseAndCredentialsAtDefaultLevel(t *testing.T) {
	operation := &refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthNone))}
	requester := &refreshRequesterFake{}
	service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthNone)}, &refreshCoordinatorFake{operation: operation}, failingRefreshDiscovery{}, requester, refreshServerID, func() time.Time { return refreshNow }, nil, nil)
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	service.SetDiagnostics(adapter, func(string) uint64 { return 1 })
	_, err := service.Refresh(t.Context(), RefreshRequest{ServerID: refreshServerID})
	require.ErrorIs(t, err, ErrTokenRejected)
	service.Shutdown()
	require.True(t, adapter.Finish(nil))
	require.Contains(t, output.String(), "lookup issuer.example: no such host")
	require.NotContains(t, output.String(), "old-access")
	require.NotContains(t, output.String(), "old-refresh")
	require.Zero(t, requester.calls)
}
