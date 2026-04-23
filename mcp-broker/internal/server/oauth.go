package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/zalando/go-keyring"
)

const keychainService = "mcp-broker"

// KeychainTokenStore implements transport.TokenStore using the OS keychain.
type KeychainTokenStore struct {
	serverName string
}

func (s *KeychainTokenStore) GetToken(ctx context.Context) (*transport.Token, error) {
	data, err := keyring.Get(keychainService, s.serverName)
	if err != nil {
		// Treat any keychain error (not found, service unavailable) as no token.
		// The OAuth flow will only trigger if the server returns 401.
		if !errors.Is(err, keyring.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "keychain unavailable for %q, proceeding without cached token: %v\n", s.serverName, err)
		}
		return nil, transport.ErrNoToken
	}
	var token transport.Token
	if err := json.Unmarshal([]byte(data), &token); err != nil {
		return nil, fmt.Errorf("unmarshal token: %w", err)
	}
	return &token, nil
}

func (s *KeychainTokenStore) SaveToken(ctx context.Context, token *transport.Token) error {
	data, err := json.Marshal(token) //nolint:gosec // token fields are not secrets in this context
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	return keyring.Set(keychainService, s.serverName, string(data))
}

// clientCreds holds the dynamic client registration (RFC 7591) issued by
// the OAuth server. Persisting it across restarts lets mcp-go reuse the
// same client_id when refreshing tokens; otherwise the server rejects the
// refresh because the stored refresh_token was bound to a prior registration.
type clientCreds struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// getClientCreds returns the stored dynamic client registration for a server,
// or (nil, nil) if none has been saved yet (first-run).
func getClientCreds(serverName string) (*clientCreds, error) {
	data, err := keyring.Get(keychainService, serverName+".client")
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get client creds for %q: %w", serverName, err)
	}
	var creds clientCreds
	if err := json.Unmarshal([]byte(data), &creds); err != nil {
		return nil, fmt.Errorf("unmarshal client creds: %w", err)
	}
	return &creds, nil
}

// saveClientCreds persists the dynamic client registration for a server.
func saveClientCreds(serverName string, creds clientCreds) error {
	data, err := json.Marshal(creds) //nolint:gosec // client_secret is intentionally persisted to the OS keychain
	if err != nil {
		return fmt.Errorf("marshal client creds: %w", err)
	}
	return keyring.Set(keychainService, serverName+".client", string(data))
}

// callbackPort returns a deterministic port for the OAuth callback server,
// derived from the server name. Maps to ephemeral range 10000-65535.
func callbackPort(serverName string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(serverName))
	return int(h.Sum32()%(65535-10000)) + 10000
}

// oauthConfig creates an OAuth config for mcp-go's transport.
// It seeds ClientID/ClientSecret from stored creds if available, so that
// refresh POSTs after restart carry the correct client_id. On keychain
// error, it logs and returns an empty-creds config (graceful degradation:
// mcp-go will re-register, which triggers a browser flow but is correct).
func oauthConfig(serverName string) transport.OAuthConfig {
	port := callbackPort(serverName)
	cfg := transport.OAuthConfig{
		RedirectURI: fmt.Sprintf("http://localhost:%d/callback", port),
		TokenStore:  &KeychainTokenStore{serverName: serverName},
		PKCEEnabled: true,
	}
	if creds, err := getClientCreds(serverName); err != nil {
		fmt.Fprintf(os.Stderr, "load client creds for %q: %v\n", serverName, err)
	} else if creds != nil {
		cfg.ClientID = creds.ClientID
		cfg.ClientSecret = creds.ClientSecret
	}
	return cfg
}

// initializeOAuthClient sends the MCP Initialize handshake, handling OAuth auth if needed.
func initializeOAuthClient(ctx context.Context, c *client.Client, name string) error {
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.1.0",
	}

	_, err := c.Initialize(ctx, initReq)
	if err == nil {
		return nil
	}

	if !client.IsOAuthAuthorizationRequiredError(err) {
		_ = c.Close()
		return fmt.Errorf("initialize server %q: %w", name, err)
	}

	if err := runOAuthFlow(ctx, err, name); err != nil {
		_ = c.Close()
		return fmt.Errorf("OAuth flow for %q: %w", name, err)
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return fmt.Errorf("initialize server %q after auth: %w", name, err)
	}
	return nil
}

// runOAuthFlow runs the interactive browser-based OAuth flow.
func runOAuthFlow(ctx context.Context, authErr error, serverName string) error {
	port := callbackPort(serverName)
	handler := client.GetOAuthHandler(authErr)
	if handler == nil {
		return fmt.Errorf("no OAuth handler in error")
	}

	// Dynamic client registration if no client ID. Persist the resulting
	// client_id/client_secret so refresh flows after restart carry the right
	// client_id — otherwise the server rejects refresh and we fall back to
	// a full browser flow daily.
	if handler.GetClientID() == "" {
		if err := handler.RegisterClient(ctx, "mcp-broker"); err != nil {
			return fmt.Errorf("register client: %w", err)
		}
		creds := clientCreds{
			ClientID:     handler.GetClientID(),
			ClientSecret: handler.GetClientSecret(),
		}
		if err := saveClientCreds(serverName, creds); err != nil {
			fmt.Fprintf(os.Stderr, "save client creds for %q: %v\n", serverName, err)
		}
	}

	// Generate PKCE verifier and challenge
	codeVerifier, err := client.GenerateCodeVerifier()
	if err != nil {
		return fmt.Errorf("generate code verifier: %w", err)
	}
	codeChallenge := client.GenerateCodeChallenge(codeVerifier)

	// Generate state for CSRF protection
	state, err := client.GenerateState()
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}

	// Get the authorization URL
	authURL, err := handler.GetAuthorizationURL(ctx, state, codeChallenge)
	if err != nil {
		return fmt.Errorf("get authorization URL: %w", err)
	}

	// Start local callback server on deterministic port
	callbackCh := make(chan callbackResult, 1)
	srv, err := startCallbackServer(port, callbackCh)
	if err != nil {
		return fmt.Errorf("start callback server: %w", err)
	}
	defer func() { _ = srv.Close() }()

	// Open browser
	fmt.Fprintf(os.Stderr, "Opening browser for OAuth authentication...\n")
	fmt.Fprintf(os.Stderr, "If the browser doesn't open, visit: %s\n", authURL)
	openBrowser(authURL)

	// Wait for callback
	fmt.Fprintf(os.Stderr, "Waiting for authentication callback on 127.0.0.1:%d...\n", port)
	var result callbackResult
	select {
	case result = <-callbackCh:
	case <-ctx.Done():
		return fmt.Errorf("OAuth flow cancelled: %w", ctx.Err())
	}

	if result.err != "" {
		return fmt.Errorf("OAuth callback error: %s", result.err)
	}

	// Exchange code for token
	if err := handler.ProcessAuthorizationResponse(ctx, result.code, state, codeVerifier); err != nil {
		return fmt.Errorf("exchange authorization code: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Authentication successful!\n")
	return nil
}

type callbackResult struct {
	code string
	err  string
}

// startCallbackServer starts a local HTTP server to receive the OAuth callback.
func startCallbackServer(port int, ch chan<- callbackResult) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		errParam := r.URL.Query().Get("error")

		switch {
		case errParam != "":
			ch <- callbackResult{err: errParam}
		case code == "":
			ch <- callbackResult{err: "no authorization code received"}
		default:
			ch <- callbackResult{code: code}
		}

		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>Authentication complete</h1><p>You can close this window.</p><script>window.close();</script></body></html>"))
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen for callback: %w", err)
	}

	go func() { _ = srv.Serve(ln) }()

	return srv, nil
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	default:
		return
	}

	_ = exec.Command(cmd, args...).Start() //nolint:gosec // args are not user-controlled
}
