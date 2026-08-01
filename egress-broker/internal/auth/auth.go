// Package auth handles the shared bearer token: generation, file storage, and
// the two very different checks the two listeners need.
//
// The token file helpers are ported from mcp-broker/internal/auth. The proxy
// listener's check is new — a forward proxy authenticates with
// Proxy-Authorization, not Authorization, and mcp-broker's Bearer-plus-cookie
// middleware carries dashboard redirect logic that has no meaning on a CONNECT.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/egress-broker/internal/paths"
)

// TokenPath returns the default token file path.
func TokenPath() string { return paths.TokenFile() }

// EnsureToken loads the token from path, generating and writing one if the
// file does not exist. The token is 32 random bytes, hex-encoded.
func EnsureToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("token file %s is empty; delete it to generate a new token", path)
		}
		return token, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}

	token, err := Generate()
	if err != nil {
		return "", err
	}
	if err := Write(path, token); err != nil {
		return "", err
	}
	return token, nil
}

// Generate returns a new random token.
func Generate() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Write stores a token with 0600 permissions.
func Write(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return fmt.Errorf("writing token file: %w", err)
	}
	return nil
}

// LoadToken reads the token from path.
func LoadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return token, nil
}

// ProxyCredential returns the Proxy-Authorization header value a client should
// send: Basic base64("x:<token>").
//
// The username is a fixed placeholder. This follows the convention used by
// other agent proxies, and matters because proxy clients in several ecosystems
// only know how to send Basic credentials embedded in the proxy URL
// (http://x:TOKEN@host:port).
func ProxyCredential(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+token))
}

// CheckProxyAuth reports whether a CONNECT or absolute-form request carries
// the correct Proxy-Authorization credential.
//
// Comparison is constant-time. Both the Basic form and a bare Bearer token are
// accepted, because not every proxy client can be made to send Basic.
func CheckProxyAuth(header, token string) bool {
	if header == "" || token == "" {
		return false
	}

	scheme, value, found := strings.Cut(header, " ")
	if !found {
		return false
	}
	value = strings.TrimSpace(value)

	switch {
	case strings.EqualFold(scheme, "Basic"):
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return false
		}
		_, password, ok := strings.Cut(string(decoded), ":")
		if !ok {
			return false
		}
		return constantTimeEqual(password, token)
	case strings.EqualFold(scheme, "Bearer"):
		return constantTimeEqual(value, token)
	default:
		return false
	}
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// DashboardCookieName is the cookie the dashboard sets after a ?token= visit.
const DashboardCookieName = "egress-broker-auth"

// CheckDashboardAuth reports whether an HTTP request to the dashboard carries
// the token, by Authorization header or by cookie.
func CheckDashboardAuth(r *http.Request, token string) bool {
	if token == "" {
		return false
	}

	if header := r.Header.Get("Authorization"); header != "" {
		scheme, value, found := strings.Cut(header, " ")
		if found && strings.EqualFold(scheme, "Bearer") && constantTimeEqual(strings.TrimSpace(value), token) {
			return true
		}
	}
	if cookie, err := r.Cookie(DashboardCookieName); err == nil {
		return constantTimeEqual(cookie.Value, token)
	}
	return false
}
