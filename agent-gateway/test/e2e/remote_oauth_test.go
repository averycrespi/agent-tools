//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
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
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/oauth"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteHTTPSOAuthThroughReverseProxy(t *testing.T) {
	var issuer, callback string
	var exchanges, calls atomic.Int64
	var mu sync.Mutex
	challenges := map[string]string{}
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": issuer + "/mcp", "authorization_servers": []string{issuer}})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "registration_endpoint": issuer + "/register", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}, "authorization_response_iss_parameter_supported": true})
		case "/register":
			var input struct {
				Redirects   []string `json:"redirect_uris"`
				Application string   `json:"application_type"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&input) != nil || len(input.Redirects) != 1 || input.Redirects[0] != callback || input.Application != "web" {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "remote-fixture", "redirect_uris": []string{callback}, "response_types": []string{"code"}, "grant_types": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_method": "none"})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("redirect_uri") != callback || q.Get("resource") != issuer+"/mcp" || q.Get("client_id") != "remote-fixture" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("code_challenge") == "" {
				w.WriteHeader(400)
				return
			}
			code := "synthetic-code-" + q.Get("state")
			mu.Lock()
			challenges[code] = q.Get("code_challenge")
			mu.Unlock()
			w.Header().Set("Location", callback+"?"+url.Values{"state": {q.Get("state")}, "code": {code}, "iss": {issuer}}.Encode())
			w.WriteHeader(302)
		case "/token":
			if r.Method != "POST" || r.ParseForm() != nil {
				w.WriteHeader(400)
				return
			}
			digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			mu.Lock()
			challenge, ok := challenges[r.Form.Get("code")]
			delete(challenges, r.Form.Get("code"))
			mu.Unlock()
			if !ok || challenge != base64.RawURLEncoding.EncodeToString(digest[:]) || r.Form.Get("redirect_uri") != callback || r.Form.Get("resource") != issuer+"/mcp" || r.Form.Get("client_id") != "remote-fixture" || r.Form.Get("grant_type") != "authorization_code" {
				w.WriteHeader(400)
				return
			}
			exchanges.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "synthetic-remote-access", "refresh_token": "synthetic-remote-refresh", "token_type": "Bearer", "expires_in": 3600})
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer synthetic-remote-access" {
				w.WriteHeader(401)
				return
			}
			var request struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&request) != nil {
				w.WriteHeader(400)
				return
			}
			switch request.Method {
			case "server/discover":
				_, _ = io.WriteString(w, rpcResult(request.ID, `{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`))
			case "tools/list":
				_, _ = io.WriteString(w, rpcResult(request.ID, `{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}`))
			case "tools/call":
				calls.Add(1)
				_, _ = io.WriteString(w, rpcResult(request.ID, `{"content":[{"type":"text","text":"remote OAuth works"}]}`))
			default:
				w.WriteHeader(400)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	certificate := filepath.Join(t.TempDir(), "fixture-ca.pem")
	require.NoError(t, os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600))
	t.Setenv("SSL_CERT_FILE", certificate)
	harness := newGatewayHarness(t)
	backend, err := url.Parse("http://" + harness.authority)
	require.NoError(t, err)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Transport: transport, Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(backend); r.Out.Host = r.In.Host }})
	defer proxy.Close()
	callback = proxy.URL + "/oauth/callback"
	harness.serveArgs = append(harness.serveArgs, "--external-origin", proxy.URL)
	harness.Start()
	browser := proxy.Client()
	browser.Timeout = 3 * time.Second
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	providerBrowser := provider.Client()
	providerBrowser.Timeout = 3 * time.Second
	providerBrowser.CheckRedirect = browser.CheckRedirect
	auth := map[string]any{"mode": "oauth", "registration": map[string]any{"mode": "dynamic", "issuer": nil}, "trusted_origins": []string{}, "request_offline_access": false}
	upstream := map[string]any{"kind": "streamable_http", "url": issuer + "/mcp", "protocol_mode": "modern", "authentication": auth}
	definition := map[string]any{"namespace": "remote-oauth", "display_name": "Remote OAuth fixture", "enabled": false, "transport": upstream}
	encoded, err := json.Marshal(definition)
	require.NoError(t, err)
	var created stdioCreation
	response := harness.AdminJSON("POST", "/api/v2/mcp/servers", string(encoded), map[string]string{"Idempotency-Key": "remote-oauth"}, &created)
	require.Equal(t, 201, response.StatusCode)
	etag := response.Header.Get("ETag")
	readResponseBody(t, response)
	serverPath := "/api/v2/mcp/servers/" + created.Server.ID
	flowPath := serverPath + "/oauth-flows"
	// Exact configured callback is the only remote override. Reject before any
	// provider work, rather than redirecting to an arbitrary origin or listener.
	for _, bad := range []string{"https://other.example/oauth/callback", proxy.URL + "/wrong", "http://localhost:3118/callback"} {
		auth["callback_uri"] = bad
		body, marshalErr := json.Marshal(map[string]any{"transport": upstream})
		require.NoError(t, marshalErr)
		response = harness.AdminJSON("PATCH", serverPath, string(body), map[string]string{"If-Match": etag}, nil)
		require.Equal(t, 400, response.StatusCode)
		readResponseBody(t, response)
	}
	delete(auth, "callback_uri")
	settle := func() {
		t.Helper()
		require.Eventually(t, func() bool {
			var server struct {
				Runtime contract.ServerRuntime `json:"runtime"`
			}
			response := harness.AdminJSON("GET", serverPath, "", nil, &server)
			readResponseBody(t, response)
			return server.Runtime.Reconciliation.InUse == 0
		}, 5*time.Second, 10*time.Millisecond)
	}
	start := func() contract.AuthFlowCreation {
		t.Helper()
		settle()
		var flow contract.AuthFlowCreation
		response := harness.AdminJSON("POST", flowPath, `{}`, map[string]string{"If-Match": etag}, &flow)
		require.Equal(t, 201, response.StatusCode)
		readResponseBody(t, response)
		return flow
	}
	authorize := func(flow contract.AuthFlowCreation) string {
		t.Helper()
		response, err := providerBrowser.Get(flow.AuthorizationURL)
		require.NoError(t, err)
		require.Equal(t, 302, response.StatusCode)
		location := response.Header.Get("Location")
		readResponseBody(t, response)
		require.True(t, strings.HasPrefix(location, callback+"?"))
		return location
	}
	visit := func(location string, status int) {
		t.Helper()
		response, err := browser.Get(location)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode)
		body := readResponseBody(t, response)
		for _, secret := range []string{"synthetic-code-", "synthetic-remote-access", "synthetic-remote-refresh"} {
			assert.NotContains(t, string(body), secret)
		}
	}
	flow := start()
	redirect := authorize(flow)
	parsed, err := url.Parse(redirect)
	require.NoError(t, err)
	// Wrong callback authority/path, forwarding metadata and encoded paths must
	// not consume valid state; the subsequent exact callback still completes.
	for _, test := range []struct{ host, path, header string }{
		{"other.example", parsed.RequestURI(), ""},
		{"", strings.Replace(parsed.RequestURI(), "/oauth/callback", "/wrong", 1), ""},
		{"", strings.Replace(parsed.RequestURI(), "/oauth/callback", "/oauth/%63allback", 1), ""},
		{"", parsed.RequestURI(), "Forwarded"},
		{"", parsed.RequestURI(), "X-Forwarded-Host"},
	} {
		// Send directly to test Gateway's rejection (ReverseProxy strips forwarding
		// fields by contract, so it cannot be used to inject this negative case).
		req, requestErr := http.NewRequestWithContext(t.Context(), "GET", backend.String()+test.path, nil)
		require.NoError(t, requestErr)
		req.Host = parsed.Host
		if test.host != "" {
			req.Host = test.host
		}
		if test.header != "" {
			req.Header.Set(test.header, "untrusted.example")
		}
		response, err = harness.client.Do(req)
		require.NoError(t, err)
		require.GreaterOrEqual(t, response.StatusCode, 400)
		readResponseBody(t, response)
	}
	q := parsed.Query()
	q.Set("state", "unknown-state")
	visit(callback+"?"+q.Encode(), 400)
	assert.Zero(t, exchanges.Load())
	visit(redirect, 200)
	visit(redirect, 400)
	require.EqualValues(t, 1, exchanges.Load())
	var completed contract.ServerAuthFlow
	response = harness.AdminJSON("GET", flowPath+"/"+flow.Flow.ID, "", nil, &completed)
	readResponseBody(t, response)
	require.Equal(t, contract.AuthFlowSucceeded, completed.FlowState)
	// Cancel and restart invalidate process-local state without code exchange.
	canceled := start()
	canceledRedirect := authorize(canceled)
	response = harness.AdminJSON("DELETE", flowPath+"/"+canceled.Flow.ID, `{}`, nil, nil)
	require.Equal(t, 204, response.StatusCode)
	readResponseBody(t, response)
	visit(canceledRedirect, 400)
	mismatched := start()
	wrongIssuer := authorize(mismatched)
	wrong, err := url.Parse(wrongIssuer)
	require.NoError(t, err)
	q = wrong.Query()
	q.Set("iss", "https://wrong.example")
	wrong.RawQuery = q.Encode()
	visit(wrong.String(), 400)
	visit(wrongIssuer, 400)
	interrupted := start()
	interruptedRedirect := authorize(interrupted)
	harness.Stop(syscall.SIGTERM)
	harness.Start()
	visit(interruptedRedirect, 400)
	response = harness.AdminJSON("GET", flowPath+"/"+interrupted.Flow.ID, "", nil, &completed)
	readResponseBody(t, response)
	require.Equal(t, contract.AuthFlowInterrupted, completed.FlowState)
	require.EqualValues(t, 1, exchanges.Load(), "invalid callbacks must not replay exchange")
	stopped := harness.Stop(syscall.SIGTERM)
	for _, secret := range []string{"synthetic-code-", "synthetic-remote-access", "synthetic-remote-refresh"} {
		assert.NotContains(t, string(stopped.Stderr), secret)
		assert.NotContains(t, string(stopped.Stdout), secret)
	}
	// Read the actual installed encrypted generation under stopped ownership.
	// The production MCP policy still refuses private HTTPS upstream addresses;
	// a test-only resolver/dial pair routes a validated public address to this
	// isolated TLS fixture without changing production policy (TOOLS-137).
	owner, err := gatewaypaths.AcquireStoppedExisting(harness.root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	store, err := storage.Open(t.Context(), owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	identity, err := store.Identity(t.Context())
	require.NoError(t, err)
	providerStore, err := keyring.NewProvider(identity.InstallationID)
	require.NoError(t, err)
	require.NoError(t, providerStore.UseDatabaseCustody(t.Context(), owner, store))
	coordinator := keyring.NewCoordinator(providerStore, store, testutil.NewFakeClock(time.Now()), rand.Reader)
	namespace, err := keyring.NewNamespace(identity.InstallationID, created.Server.ID, keyring.RecordOAuthTokens)
	require.NoError(t, err)
	contents, _, err := coordinator.ReadActive(t.Context(), namespace)
	require.NoError(t, err)
	defer clear(contents)
	generation, err := oauth.DecodeTokenGeneration(contents)
	require.NoError(t, err)
	require.Equal(t, issuer+"/mcp", generation.Resource)
	endpoint, err := remote.Parse(issuer+"/mcp", remote.Policy{})
	require.NoError(t, err)
	factory := remote.New(remote.Options{Resolver: remoteOAuthFixtureResolver{}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		require.Equal(t, "8.8.8.8", strings.Split(address, ":")[0])
		dialer := net.Dialer{Timeout: time.Second}
		return dialer.DialContext(ctx, network, provider.Listener.Addr().String())
	}})
	result, err := factory.Exchange(t.Context(), remote.Request{Endpoint: endpoint, Method: "POST", Header: http.Header{"Authorization": {"Bearer " + generation.AccessToken}, "Content-Type": {contract.MediaTypeJSON}}, Body: []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`), MaxBody: 16384})
	require.NoError(t, err)
	require.Equal(t, 200, result.StatusCode)
	require.Contains(t, string(result.Body), "remote OAuth works")
	require.EqualValues(t, 1, calls.Load())
}

type remoteOAuthFixtureResolver struct{}

func (remoteOAuthFixtureResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}
