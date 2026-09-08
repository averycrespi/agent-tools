package composition

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthCallbackListenerPanicCannotReflectSecrets(t *testing.T) {
	owner := &oauthCallbackListeners{leases: make(map[string]*oauthCallbackListener), changed: make(chan struct{}), connections: make(chan struct{}, 16), now: time.Now, handle: func(_ context.Context, query, _, _ string) oauth.CallbackResult { panic(query) }, notify: func(context.Context, oauth.CallbackResult) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() { require.True(t, owner.Wait(ctx)) }()
	release, err := owner.AcquireCallback(ctx, "panic-flow", "http://127.0.0.1:3118/callback", time.Now().Add(time.Minute))
	require.NoError(t, err)
	defer release()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get("http://127.0.0.1:3118/callback?state=secret-canary&code=secret-canary")
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.NotContains(t, string(body), "secret-canary")
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	require.True(t, owner.Wait(ctx))
	listener, err := net.Listen("tcp4", "127.0.0.1:3118")
	require.NoError(t, err)
	require.NoError(t, listener.Close())
}

func TestOAuthCallbackListenerExpiresWithoutPolling(t *testing.T) {
	owner := &oauthCallbackListeners{leases: make(map[string]*oauthCallbackListener), changed: make(chan struct{}), connections: make(chan struct{}, 16), now: time.Now, handle: func(context.Context, string, string, string) oauth.CallbackResult {
		return oauth.CallbackResult{Outcome: oauth.CallbackInvalid}
	}, notify: func(context.Context, oauth.CallbackResult) {}}
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	address := reserved.Addr().String()
	require.NoError(t, reserved.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := owner.AcquireCallback(ctx, "expiring", "http://"+address+"/callback", time.Now().Add(100*time.Millisecond))
	require.NoError(t, err)
	defer release()
	owner.mu.Lock()
	changed := owner.changed
	expired := len(owner.leases) == 0
	owner.mu.Unlock()
	if !expired {
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("callback listener did not expire")
		}
	}
	require.True(t, owner.Wait(ctx))
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
}

func TestOAuthCallbackListenerAdmissionCollisionAndCleanup(t *testing.T) {
	owner := &oauthCallbackListeners{leases: make(map[string]*oauthCallbackListener), changed: make(chan struct{}), connections: make(chan struct{}, 16), now: time.Now}
	var admitted atomic.Int64
	owner.handle = func(_ context.Context, query, uri, id string) oauth.CallbackResult {
		admitted.Add(1)
		assert.Equal(t, "state=fixture", query)
		assert.Equal(t, "http://localhost:3118/callback", uri)
		assert.Equal(t, "fixture-flow", id)
		return oauth.CallbackResult{Outcome: oauth.CallbackSucceeded}
	}
	owner.notify = func(context.Context, oauth.CallbackResult) {}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() { require.True(t, owner.Wait(ctx), "callback listener cleanup was not confirmed") }()
	release, err := owner.AcquireCallback(ctx, "fixture-flow", "http://localhost:3118/callback", time.Now().Add(time.Minute))
	require.NoError(t, err, "exact port 3118 is required; a collision must not substitute another port")
	defer release()
	_, err = owner.AcquireCallback(ctx, "conflicting-flow", "http://localhost:3118/callback", time.Now().Add(time.Minute))
	require.ErrorIs(t, err, errCallbackListener)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, test := range []struct {
		method, path, host, forwarding string
		status                         int
	}{
		{"GET", "/callback?state=fixture", "localhost:3118", "", 200},
		{"POST", "/callback?state=fixture", "localhost:3118", "", 400},
		{"GET", "/mcp", "localhost:3118", "", 400},
		{"GET", "/api/v1/status", "localhost:3118", "", 400},
		{"GET", "/healthz", "localhost:3118", "", 400},
		{"GET", "/", "localhost:3118", "", 400},
		{"GET", "/callback?state=fixture", "127.0.0.1:3118", "", 400},
		{"GET", "/callback?state=fixture", "localhost:3119", "", 400},
		{"GET", "/callback?state=fixture", "localhost:3118", "host=localhost", 400},
		{"GET", "/%63allback?state=fixture", "localhost:3118", "", 400},
	} {
		req, err := http.NewRequestWithContext(ctx, test.method, "http://127.0.0.1:3118"+test.path, nil)
		require.NoError(t, err)
		req.Host = test.host
		if test.forwarding != "" {
			req.Header.Set("Forwarded", test.forwarding)
		}
		response, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, test.status, response.StatusCode)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		assert.NotContains(t, string(body), "fixture")
		assert.Empty(t, response.Header.Get("Location"))
	}
	assert.EqualValues(t, 1, admitted.Load())
	release()
	require.True(t, owner.Wait(ctx))
	listener, err := net.Listen("tcp4", "127.0.0.1:3118")
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	_, err = owner.AcquireCallback(ctx, "after-shutdown", "http://localhost:3118/callback", time.Now().Add(time.Minute))
	require.ErrorIs(t, err, errCallbackListener)
}
