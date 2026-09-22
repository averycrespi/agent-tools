package composition

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/stretchr/testify/require"
)

func TestOptionalHTTPProxyCompositionAndDynamicListenerExclusion(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := newWithHooks(options, constructorHooks{provider: func(id string) (*keyring.Provider, error) {
		return keyring.NewProviderWithBackend(id, newMemoryBackend())
	}})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	require.Nil(t, built.httpProxy, "ordinary construction must not select an engine")
	ctx := audit.WithSystem(t.Context())
	require.NoError(t, built.httpCA.Replace(ctx, "0"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := netip.MustParseAddrPort(listener.Addr().String())
	engine, err := built.PrepareHTTPProxy(ctx, netip.MustParseAddrPort("127.0.0.1:8210"), endpoint)
	require.NoError(t, err)
	require.NoError(t, built.Start(ctx))
	require.True(t, built.HTTPProxyStatus().Ready)
	done := make(chan error, 1)
	go func() { done <- engine.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, engine.Close(ctx))
		require.ErrorIs(t, <-done, http.ErrServerClosed)
	}()
	principal, err := built.authorization.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "Proxy", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	credential, err := built.authorization.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = io.WriteString(w, "shared owners") }))
	defer upstream.Close()
	address := netip.MustParseAddrPort(upstream.Listener.Addr().String())
	private := true
	policy, err := json.Marshal(contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowRequests, AllowPrivate: &private, Request: &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: "http", Host: address.Addr().String(), Port: address.Port()}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}})
	require.NoError(t, err)
	_, err = built.authorization.PutHTTPGrant(ctx, "", "", authorization.HTTPGrantInput{PrincipalID: principal.Principal.ID, Policy: policy})
	require.NoError(t, err)
	proxyURL := &url.URL{Scheme: "http", Host: listener.Addr().String(), User: url.UserPassword("agent", credential.Bearer)}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request := func() int {
		r, err := http.NewRequestWithContext(ctx, "GET", upstream.URL+"/", nil)
		require.NoError(t, err)
		response, err := client.Do(r)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		return response.StatusCode
	}
	require.Equal(t, 200, request())
	built.oauthCallbacks.mu.Lock()
	built.oauthCallbacks.leases["reserved-fixture"] = &oauthCallbackListener{address: address}
	built.oauthCallbacks.mu.Unlock()
	require.Equal(t, 403, request())
	require.EqualValues(t, 1, calls.Load())
	built.oauthCallbacks.mu.Lock()
	delete(built.oauthCallbacks.leases, "reserved-fixture")
	built.oauthCallbacks.mu.Unlock()
}
