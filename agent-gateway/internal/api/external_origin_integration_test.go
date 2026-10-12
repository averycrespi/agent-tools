//go:build integration

package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/events"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

var remoteSessionTemplate = storagefixture.New(testID)

type remoteSessionSink struct{ value string }

func (s *remoteSessionSink) Publish(value string) error { s.value = value; return nil }

func TestIntegrationExternalOriginSessionsThroughHTTPSProxy(t *testing.T) {
	owner, err := gatewaypaths.Acquire(t.TempDir())
	require.NoError(t, err)
	defer owner.Close()
	store, err := remoteSessionTemplate.Open(t.Context(), owner)
	require.NoError(t, err)
	defer store.Close()
	service := admin.NewService(store, httpCredentialClock{}, rand.Reader)
	sink := new(remoteSessionSink)
	_, err = service.Initialize(t.Context(), sink)
	require.NoError(t, err)
	sessions := admin.NewSessionManager(service, httpCredentialClock{}, rand.Reader)
	defer sessions.Shutdown()
	hub := events.New()
	defer hub.Shutdown()
	// The proxy preserves Host/Origin and strips forwarding metadata; no vendor
	// headers or TLS state from the internal HTTP hop select browser authority.
	var backendURL *url.URL
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(backendURL)
		r.Out.Host = r.In.Host
	}})
	defer proxy.Close()
	origin := proxy.URL
	public, err := url.Parse(origin)
	require.NoError(t, err)
	handler := New(Options{Origin: origin, Credentials: service, Sessions: sessions, Events: hub})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: "0.0.0.0:8210", ExternalOrigin: origin, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	backend := httptest.NewServer(boundary)
	defer backend.Close()
	backendURL, err = url.Parse(backend.URL)
	require.NoError(t, err)
	client := proxy.Client()
	client.Timeout = 5 * time.Second
	call := func(method, path, body, bearer, cookie, csrf, requestOrigin string) *http.Response {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, origin+path, strings.NewReader(body))
		require.NoError(t, err)
		r.Host = public.Host
		r.Header.Set("Content-Type", contract.MediaTypeJSON)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		response, err := client.Do(r)
		require.NoError(t, err)
		t.Cleanup(func() { response.Body.Close() })
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		require.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))
		return response
	}
	legacy := call("POST", "/api/v2/admin-sessions", "{}", "invalid", contract.LegacySessionCookieName+"=retired", "", origin)
	require.Equal(t, 401, legacy.StatusCode)
	require.Len(t, legacy.Cookies(), 1)
	require.Equal(t, contract.LegacySessionCookieName, legacy.Cookies()[0].Name)
	require.True(t, legacy.Cookies()[0].Secure)
	legacy.Body.Close()
	signed := call("POST", "/api/v2/admin-sessions", "{}", sink.value, "", "", origin)
	require.Equal(t, 201, signed.StatusCode)
	cookies := signed.Cookies()
	require.Len(t, cookies, 1)
	cookie := cookies[0]
	require.True(t, cookie.Secure)
	require.True(t, cookie.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	require.Empty(t, cookie.Domain)
	var created contract.AdminSessionCreated
	require.NoError(t, json.NewDecoder(signed.Body).Decode(&created))
	signed.Body.Close()
	pair := cookie.Name + "=" + cookie.Value
	bootstrap := call("POST", "/api/v2/admin-sessions/current", "{}", "", pair, "", origin)
	require.Equal(t, 200, bootstrap.StatusCode)
	var recovered contract.AdminSessionBootstrap
	require.NoError(t, json.NewDecoder(bootstrap.Body).Decode(&recovered))
	bootstrap.Body.Close()
	require.Equal(t, created.CSRFToken, recovered.CSRFToken)
	for _, tc := range []struct {
		name, csrf, origin string
		status             int
	}{
		{"wrong csrf", "wrong", origin, 403}, {"missing origin", created.CSRFToken, "", 403}, {"wrong scheme", created.CSRFToken, strings.Replace(origin, "https:", "http:", 1), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := call("DELETE", "/api/v2/admin-sessions/current", "{}", "", pair, tc.csrf, tc.origin)
			require.Equal(t, tc.status, r.StatusCode)
		})
	}
	read := call("GET", "/api/v2/admin-credentials", "", "", pair, created.CSRFToken, "")
	require.Equal(t, 200, read.StatusCode)
	read.Body.Close()
	bearerRead := call("GET", "/api/v2/admin-credentials", "", sink.value, "", "", "")
	require.Equal(t, 200, bearerRead.StatusCode)
	bearerRead.Body.Close()
	// A stream must flush its initial frame through the same generic proxy.
	streamCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(streamCtx, "POST", origin+"/api/v2/events", strings.NewReader("{}"))
	require.NoError(t, err)
	r.Header.Set("Content-Type", contract.MediaTypeJSON)
	r.Header.Set("Origin", origin)
	r.Header.Set("Cookie", pair)
	r.Header.Set("X-CSRF-Token", created.CSRFToken)
	stream, err := client.Do(r)
	require.NoError(t, err)
	require.Equal(t, 200, stream.StatusCode)
	initial := make([]byte, 1)
	_, err = io.ReadFull(stream.Body, initial)
	require.NoError(t, err)
	cancel()
	stream.Body.Close()
	logout := call("DELETE", "/api/v2/admin-sessions/current", "{}", "", pair, created.CSRFToken, origin)
	require.Equal(t, 204, logout.StatusCode)
	require.True(t, logout.Cookies()[0].Secure)
	expired := call("POST", "/api/v2/admin-sessions/current", "{}", "", pair, "", origin)
	require.Equal(t, 401, expired.StatusCode)
	require.True(t, expired.Cookies()[0].Secure)
}
