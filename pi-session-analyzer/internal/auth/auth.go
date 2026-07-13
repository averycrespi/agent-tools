// Package auth persists and checks the dashboard bearer token. It mirrors
// mcp-broker's auth token: 32 random bytes hex-encoded (64 chars), stored at
// a 0600 file under XDG config, compared in constant time. The token is
// defense-in-depth; the loopback bind remains the load-bearing boundary.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CookieName carries the dashboard session token after the launch URL's
// query token is exchanged and cleared.
const CookieName = "pi-session-analyzer-auth"

// TokenPath returns the default token file path under the XDG config directory.
func TokenPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "pi-session-analyzer", "auth-token")
}

// EnsureToken loads the token from path, or generates and writes a new one if
// the file doesn't exist. Returns the 64-character hex token string.
func EnsureToken(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // The caller-selected local token path is the intended file.
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return Rotate(path)
}

// Rotate generates a fresh token and overwrites the file at path.
func Rotate(path string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("writing token file: %w", err)
	}
	return token, nil
}

// Equal compares a provided value against the token in constant time.
func Equal(provided, token string) bool {
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// CheckBearer reports whether the request carries the token as an
// Authorization: Bearer header.
func CheckBearer(r *http.Request, token string) bool {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	return Equal(strings.TrimPrefix(header, "Bearer "), token)
}

// CheckCookie reports whether the request carries the token in the auth cookie.
func CheckCookie(r *http.Request, token string) bool {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	return Equal(cookie.Value, token)
}

// SetCookie stores the token in the auth cookie after a valid query-token
// exchange.
func SetCookie(w http.ResponseWriter, token string) {
	//nolint:gosec // The dashboard is loopback-only plain HTTP; a Secure cookie would prevent local auth from working.
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(365 * 24 * time.Hour / time.Second),
	})
}

// RedirectWithoutToken redirects to the same URL with the token query
// parameter removed, so the secret never stays in the address bar or history.
func RedirectWithoutToken(w http.ResponseWriter, r *http.Request) {
	clean := *r.URL
	query := clean.Query()
	query.Del("token")
	clean.RawQuery = query.Encode()
	//nolint:gosec // RequestURI is a relative same-origin redirect, not an external target.
	http.Redirect(w, r, clean.RequestURI(), http.StatusFound)
}
