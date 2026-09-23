//go:build e2e

package e2e

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func putProxyTestGrant(t *testing.T, h *gatewayHarness, principal string, policy contract.HTTPPolicy) responseSnapshot {
	t.Helper()
	body, err := json.Marshal(map[string]any{"principal_id": principal, "description": nil, "expires_at": nil, "policy": policy})
	require.NoError(t, err)
	response := h.adminSnapshot("POST", "/api/v2/http/grants", body)
	require.Equal(t, 201, response.StatusCode, "create fixture grant: %s", response.Body)
	return response
}

func TestHTTPProxyInterceptedStreamingRotationAndFourPriorities(t *testing.T) {
	h := newGatewayHarness(t)
	h.binary, _ = httpMaterialBinary(t)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	var calls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Proxy-Authorization") != "" {
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: admitted\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
				_, _ = io.WriteString(w, "data: completed\n\n")
			case <-r.Context().Done():
			}
			return
		}
		if r.Header.Get("X-Fixture-Key") == "" {
			_, _ = io.WriteString(w, "uninjected")
			return
		}
		if r.Header.Get("X-Fixture-Key") != "fixture-material-two" {
			w.WriteHeader(500)
			return
		}
		_, _ = io.WriteString(w, "injected")
	}))
	defer upstream.Close()
	trust := filepath.Join(t.TempDir(), "upstream-ca.pem")
	require.NoError(t, os.WriteFile(trust, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0o600))
	t.Setenv("SSL_CERT_FILE", trust)
	ca := createHTTPCA(t, h)
	proxyAuthority := unusedAuthority(t)
	h.serveArgs = append(h.serveArgs, "--http-proxy-listen", proxyAuthority)
	h.Start()
	defer func() {
		finish()
		if h.process != nil {
			h.Stop(syscall.SIGTERM)
		}
	}()
	principal := h.CreatePrincipal("Streams", contract.VisibilityRequestable)
	credential := h.IssueCredential(principal)
	endpoint := netip.MustParseAddrPort(upstream.Listener.Addr().String())
	private := true
	selector := &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: "https", Host: endpoint.Addr().String(), Port: endpoint.Port()}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}
	materialBody, err := json.Marshal(map[string]any{"name": "Fixture key", "boundary": map[string]any{"host": endpoint.Addr().String(), "port": endpoint.Port(), "allow_wildcard": false}, "recipe": map[string]any{"header": "X-Fixture-Key", "prefix": ""}, "secret": "fixture-material-one"})
	require.NoError(t, err)
	material := h.adminSnapshot("POST", "/api/v2/http/credentials", materialBody)
	require.Equal(t, 201, material.StatusCode)
	var metadata contract.HTTPCredential
	require.NoError(t, json.Unmarshal(material.Body, &metadata))
	rotated := h.adminSnapshotWithHeaders("POST", "/api/v2/http/credentials/"+metadata.ID+"/rotate", []byte(`{"secret":"fixture-material-two"}`), map[string]string{"If-Match": material.Header.Get("ETag")})
	require.Equal(t, 200, rotated.StatusCode)
	putProxyTestGrant(t, h, principal.Resource.ID, contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowRequests, Request: selector, AllowPrivate: &private, CredentialID: &metadata.ID})
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(ca))
	roots.AddCert(upstream.Certificate())
	client := func(bearer *agentBearer) *http.Client {
		proxy := &url.URL{Scheme: "http", Host: proxyAuthority, User: url.UserPassword("agent", strings.TrimPrefix(bearer.authorizationHeader(), "Bearer "))}
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}} //nolint:gosec // Exercise supported TLS 1.2 clients with verified fixture trust.
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 5 * time.Second}
	}
	oldClient := client(credential.Bearer)
	response, err := oldClient.Get(upstream.URL + "/stream")
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	require.Equal(t, 2, response.ProtoMajor)
	first := make([]byte, len("data: admitted\n\n"))
	_, err = io.ReadFull(response.Body, first)
	require.NoError(t, err)
	replacement := h.IssueCredential(credential.Principal)
	finish()
	rest, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Contains(t, string(rest), "data: completed")
	before := calls.Load()
	rejected, err := oldClient.Get(upstream.URL + "/new-admission")
	if err == nil {
		require.Equal(t, 407, rejected.StatusCode)
		require.NoError(t, rejected.Body.Close())
	}
	require.Equal(t, before, calls.Load(), "revoked credentials must never reach the upstream")
	fresh := client(replacement.Bearer)
	read := func(expected int) *http.Response {
		r, err := fresh.Get(upstream.URL + "/private-path?private-query=omitted")
		require.NoError(t, err)
		require.Equal(t, expected, r.StatusCode)
		return r
	}
	response = read(200)
	body := readResponseBody(t, response)
	require.Equal(t, "injected", string(body))
	require.NotEqual(t, upstream.Certificate().Raw, response.TLS.PeerCertificates[0].Raw, "intercepted client sees the Gateway-issued leaf")
	putProxyTestGrant(t, h, principal.Resource.ID, contract.HTTPPolicy{Version: 1, Type: contract.HTTPBlockRequests, Request: selector})
	response = read(403)
	_ = readResponseBody(t, response)
	// New connections select the tunnel grant rather than reusing intercepted TLS.
	fresh.CloseIdleConnections()
	destination := &contract.HTTPDestinationSelector{Host: endpoint.Addr().String(), Port: endpoint.Port()}
	putProxyTestGrant(t, h, principal.Resource.ID, contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowTunnel, Destination: destination, AllowPrivate: &private})
	response = read(200)
	body = readResponseBody(t, response)
	require.Equal(t, "uninjected", string(body))
	require.Equal(t, upstream.Certificate().Raw, response.TLS.PeerCertificates[0].Raw)
	fresh.CloseIdleConnections()
	putProxyTestGrant(t, h, principal.Resource.ID, contract.HTTPPolicy{Version: 1, Type: contract.HTTPBlockDestination, Destination: destination})
	before = calls.Load()
	blocked, err := fresh.Get(upstream.URL + "/")
	if err == nil {
		require.Equal(t, 403, blocked.StatusCode)
		require.NoError(t, blocked.Body.Close())
	}
	require.Equal(t, before, calls.Load(), "destination block overrides tunnel permission")
	history := h.adminSnapshot("GET", "/api/v2/http/traffic", nil)
	require.Equal(t, 200, history.StatusCode)
	for _, canary := range []string{"fixture-material-one", "fixture-material-two", "private-path", "private-query"} {
		require.NotContains(t, string(history.Body), canary)
	}
	credential.Bearer.assertAbsent(t, "HTTP history", strings.NewReader(string(history.Body)))
	replacement.Bearer.assertAbsent(t, "HTTP history", strings.NewReader(string(history.Body)))
}
