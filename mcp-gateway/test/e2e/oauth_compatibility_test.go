//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackShapedOAuthCompatibility(t *testing.T) {
	const callback = "http://localhost:3118/callback"
	const scopes = "fixture.read fixture.write"
	var issuer string
	var metadataVisits, exchanges, refreshes atomic.Int64
	var mu sync.Mutex
	var challenge string
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": issuer + "/mcp", "authorization_servers": []string{issuer}, "scopes_supported": []string{"metadata.only"}})
		case "/custom/metadata":
			metadataVisits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "registration_endpoint": issuer + "/register", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}, "authorization_response_iss_parameter_supported": true})
		case "/register":
			var input struct {
				Redirects []string `json:"redirect_uris"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&input) != nil || len(input.Redirects) != 1 || input.Redirects[0] != callback {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "fixture-client", "redirect_uris": []string{callback}, "response_types": []string{"code"}, "grant_types": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_method": "none"})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("redirect_uri") != callback || q.Get("client_id") != "fixture-client" || q.Get("resource") != issuer+"/mcp" || q.Get("scope") != scopes || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("code_challenge") == "" {
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			challenge = q.Get("code_challenge")
			mu.Unlock()
			w.Header().Set("Location", callback+"?"+url.Values{"state": {q.Get("state")}, "code": {"fixture-code"}, "iss": {issuer}}.Encode())
			w.WriteHeader(302)
		case "/token":
			if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("client_id") != "fixture-client" || r.Form.Get("resource") != issuer+"/mcp" {
				w.WriteHeader(400)
				return
			}
			access, refresh := "fixture-access-1", "fixture-refresh-1"
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				mu.Lock()
				matches := base64.RawURLEncoding.EncodeToString(digest[:]) == challenge
				mu.Unlock()
				if !matches || r.Form.Get("code") != "fixture-code" || r.Form.Get("redirect_uri") != callback {
					w.WriteHeader(400)
					return
				}
				exchanges.Add(1)
			case "refresh_token":
				if r.Form.Get("refresh_token") != "fixture-refresh-1" {
					w.WriteHeader(400)
					return
				}
				refreshes.Add(1)
				access, refresh = "fixture-access-2", "fixture-refresh-2"
			default:
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "scope": scopes, "expires_in": 3600})
		default:
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	certificate := filepath.Join(t.TempDir(), "fixture-ca.pem")
	require.NoError(t, os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0o600))
	t.Setenv("SSL_CERT_FILE", certificate)
	harness := newGatewayHarness(t)
	harness.serveArgs = append(harness.serveArgs, "--log-level", "debug")
	harness.Start()
	auth := map[string]any{"mode": "oauth", "registration": map[string]any{"mode": "dynamic", "issuer": nil}, "trusted_origins": []string{}, "request_offline_access": false}
	transport := map[string]any{"kind": "streamable_http", "url": issuer + "/mcp", "protocol_mode": "modern", "authentication": auth}
	encoded, err := json.Marshal(map[string]any{"namespace": "slack-shaped", "display_name": "Synthetic compatibility", "enabled": false, "transport": transport})
	require.NoError(t, err)
	var created stdioCreation
	response := harness.AdminJSON("POST", "/api/v1/servers", string(encoded), map[string]string{"Idempotency-Key": "slack-shaped"}, &created)
	require.Equal(t, 201, response.StatusCode)
	etag := response.Header.Get("ETag")
	require.NoError(t, response.Body.Close())
	flowPath := "/api/v1/servers/" + created.Server.ID + "/auth-flows"
	response = harness.AdminJSON("POST", flowPath, `{}`, map[string]string{"If-Match": etag}, nil)
	require.NotEqual(t, 201, response.StatusCode, "standard discovery must not pass this provider")
	require.NoError(t, response.Body.Close())
	assert.Zero(t, metadataVisits.Load())
	var standardFlows contract.Collection[contract.ServerAuthFlow]
	response = harness.AdminJSON("GET", flowPath, "", nil, &standardFlows)
	require.NoError(t, response.Body.Close())
	require.Len(t, standardFlows.Items, 1)
	require.NotNil(t, standardFlows.Items[0].Diagnostic)
	assert.Equal(t, contract.OAuthDiagnosticMetadataDiscovery, standardFlows.Items[0].Diagnostic.Stage)
	require.NotNil(t, standardFlows.Items[0].Diagnostic.HTTPStatus)
	assert.Equal(t, 404, *standardFlows.Items[0].Diagnostic.HTTPStatus)
	patch := func() {
		t.Helper()
		encoded, err := json.Marshal(map[string]any{"transport": transport})
		require.NoError(t, err)
		var result stdioCreation
		response := harness.AdminJSON("PATCH", "/api/v1/servers/"+created.Server.ID, string(encoded), map[string]string{"If-Match": etag}, &result)
		require.Equal(t, 200, response.StatusCode)
		etag = response.Header.Get("ETag")
		require.NoError(t, response.Body.Close())
		if result.Operation != nil {
			harness.WaitSettledOperation(created.Server.ID, result.Operation.ID)
		}
	}
	auth["callback_uri"], auth["auth_server_metadata_url"], auth["scopes"] = callback, issuer+"/custom/metadata", []string{"fixture.write", "fixture.read"}
	patch()
	readAuthentication := func() map[string]any {
		var snapshot struct {
			Transport struct {
				Authentication map[string]any `json:"authentication"`
			} `json:"transport"`
		}
		response = harness.AdminJSON("GET", "/api/v1/servers/"+created.Server.ID, "", nil, &snapshot)
		require.Equal(t, 200, response.StatusCode)
		require.NoError(t, response.Body.Close())
		return snapshot.Transport.Authentication
	}
	persisted := readAuthentication()
	assert.Equal(t, callback, persisted["callback_uri"])
	assert.Equal(t, issuer+"/custom/metadata", persisted["auth_server_metadata_url"])
	assert.Equal(t, []any{"fixture.read", "fixture.write"}, persisted["scopes"])
	start := func() contract.AuthFlowCreation {
		t.Helper()
		var flow contract.AuthFlowCreation
		response := harness.AdminJSON("POST", flowPath, `{}`, map[string]string{"If-Match": etag}, nil)
		if response.StatusCode != http.StatusCreated {
			body := readResponseBody(t, response)
			t.Fatalf("create OAuth flow: status=%d body=%s", response.StatusCode, body)
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&flow))
		require.NoError(t, response.Body.Close())
		return flow
	}
	occupied, err := net.Listen("tcp4", "127.0.0.1:3118")
	require.NoError(t, err, "exact port 3118 must be available for the collision fixture")
	t.Cleanup(func() { _ = occupied.Close() })
	var collision contract.ProblemEnvelope
	response = harness.AdminJSON("POST", flowPath, `{}`, map[string]string{"If-Match": etag}, &collision)
	require.Equal(t, 409, response.StatusCode)
	assert.Equal(t, contract.ProblemOAuthCallbackUnavailable, collision.Code)
	require.NoError(t, response.Body.Close())
	require.NoError(t, occupied.Close())
	stale := start()
	for _, field := range []string{"callback_uri", "auth_server_metadata_url", "scopes"} {
		delete(auth, field)
	}
	patch()
	cleared := readAuthentication()
	for _, field := range []string{"callback_uri", "auth_server_metadata_url", "scopes"} {
		assert.NotContains(t, cleared, field)
	}
	var terminal contract.ServerAuthFlow
	response = harness.AdminJSON("GET", flowPath+"/"+stale.Flow.ID, "", nil, &terminal)
	require.Equal(t, 200, response.StatusCode)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, contract.AuthFlowSuperseded, terminal.FlowState)
	staleURL, err := url.Parse(stale.AuthorizationURL)
	require.NoError(t, err)
	response = harness.Request("GET", "/oauth/callback?"+url.Values{"state": {staleURL.Query().Get("state")}, "code": {"fixture-code"}, "iss": {issuer}}.Encode(), "", nil)
	assert.Equal(t, 400, response.StatusCode)
	require.NoError(t, response.Body.Close())
	assert.Zero(t, exchanges.Load())
	listener, err := net.Listen("tcp4", "127.0.0.1:3118")
	require.NoError(t, err, "supersession must release port 3118")
	require.NoError(t, listener.Close())
	auth["callback_uri"], auth["auth_server_metadata_url"], auth["scopes"] = callback, issuer+"/custom/metadata?revision=2", []string{"fixture.read", "fixture.write"}
	patch()
	flow := start()
	client := provider.Client()
	client.Timeout = 3 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err = client.Get(flow.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, 302, response.StatusCode)
	redirect, err := url.Parse(response.Header.Get("Location"))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	request, err := http.NewRequestWithContext(harness.ctx, "GET", "http://127.0.0.1:3118"+redirect.RequestURI(), nil)
	require.NoError(t, err)
	request.Host = "localhost:3118"
	response, err = harness.client.Do(request)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	require.NoError(t, response.Body.Close())
	var completed contract.ServerAuthFlow
	response = harness.AdminJSON("GET", flowPath+"/"+flow.Flow.ID, "", nil, &completed)
	require.Equal(t, 200, response.StatusCode)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, contract.AuthFlowSucceeded, completed.FlowState)
	var installed struct {
		CredentialRevisions contract.CredentialRevisions `json:"credential_revisions"`
	}
	response = harness.AdminJSON("GET", "/api/v1/servers/"+created.Server.ID, "", nil, &installed)
	require.Equal(t, 200, response.StatusCode)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "1", installed.CredentialRevisions.OAuthTokens)
	assert.EqualValues(t, 1, exchanges.Load())
	assert.EqualValues(t, 3, metadataVisits.Load())
	listener, err = net.Listen("tcp4", "127.0.0.1:3118")
	require.NoError(t, err, "completion must release port 3118")
	require.NoError(t, listener.Close())
	result := harness.Stop(syscall.SIGTERM)
	for _, event := range []string{"oauth_required", "oauth_failed", "oauth_completed"} {
		require.Contains(t, string(result.Stderr), `"event":"`+event+`"`)
	}
	for _, canary := range []string{issuer, "fixture-access-1", "fixture-refresh-1", "fixture-code", "fixture-client", created.Server.ID, flow.Flow.ID, redirect.Query().Get("state"), flow.AuthorizationURL} {
		require.NotEmpty(t, canary)
		require.NotContains(t, string(result.Stderr), canary)
	}
	assertOAuthDiagnosticSequence(t, result.Stderr)
}
