// Package dashboard implements the admin web UI for agent-gateway.
package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/atomicfile"
)

const cookieName = "agent-gateway-auth"

// EnsureAdminToken loads the admin token from path, or generates and writes a
// new one if the file does not exist. Returns the 64-character hex token.
func EnsureAdminToken(path string) (string, error) {
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		return strings.TrimSpace(string(data)), nil
	}
	if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("reading admin token file: %w", readErr)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating admin token: %w", err)
	}
	tok := hex.EncodeToString(b)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating token directory: %w", err)
	}
	if err := atomicfile.Write(path, []byte(tok), 0o600); err != nil {
		return "", fmt.Errorf("writing admin token file: %w", err)
	}
	return tok, nil
}

// GenerateAdminToken generates a new random token, overwrites the file at
// path, and returns the new 64-character hex token.
func GenerateAdminToken(path string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating admin token: %w", err)
	}
	tok := hex.EncodeToString(b)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating token directory: %w", err)
	}
	if err := atomicfile.Write(path, []byte(tok), 0o600); err != nil {
		return "", fmt.Errorf("writing admin token file: %w", err)
	}
	return tok, nil
}

// authMiddleware wraps next, enforcing admin token auth for /dashboard/* paths.
// /ca.pem and /dashboard/unauthorized are allowlisted (no auth required).
// The token may be supplied as:
//  1. Authorization: Bearer <token> header.
//  2. The agent-gateway-auth cookie.
//  3. ?token=<tok> query param (dashboard only): sets cookie and redirects.
//
// Any dashboard request without valid auth is redirected to /dashboard/unauthorized.
// Non-dashboard requests (future API endpoints not under /dashboard/) receive 401.
//
// tokenPtr is read atomically on every request so a SIGHUP-triggered token
// rotation takes effect immediately without restarting the server.
func authMiddleware(tokenPtr *atomic.Pointer[string], next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Allowlisted: /ca.pem and /dashboard/unauthorized.
		if path == "/ca.pem" || path == "/dashboard/unauthorized" {
			next.ServeHTTP(w, r)
			return
		}

		// Load the current token atomically on every request.
		cur := tokenPtr.Load()
		var tokenBytes []byte
		if cur != nil {
			tokenBytes = []byte(*cur)
		}

		// Check Authorization: Bearer header.
		if checkBearer(r, tokenBytes) {
			next.ServeHTTP(w, r)
			return
		}

		// Check cookie.
		if checkCookie(r, tokenBytes) {
			next.ServeHTTP(w, r)
			return
		}

		isDashboard := strings.HasPrefix(path, "/dashboard")

		// Dashboard with ?token= query param: set cookie and redirect.
		if isDashboard {
			if qToken := r.URL.Query().Get("token"); qToken != "" {
				if subtle.ConstantTimeCompare([]byte(qToken), tokenBytes) == 1 {
					token := ""
					if cur != nil {
						token = *cur
					}
					http.SetCookie(w, &http.Cookie{
						Name:     cookieName,
						Value:    token,
						Path:     "/dashboard/",
						HttpOnly: true,
						SameSite: http.SameSiteStrictMode,
						MaxAge:   int(365 * 24 * time.Hour / time.Second),
						// Secure is false for local dev (127.0.0.1 over HTTP).
						// TODO(TLS): set Secure: true when the gateway is served over HTTPS.
						Secure: false,
					})
					clean := *r.URL
					q := clean.Query()
					q.Del("token")
					clean.RawQuery = q.Encode()
					http.Redirect(w, r, clean.RequestURI(), http.StatusFound)
					return
				}
			}

			// No valid auth: redirect to unauthorized page.
			http.Redirect(w, r, "/dashboard/unauthorized", http.StatusFound)
			return
		}

		// Non-dashboard: 401.
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
}

// checkBearer and checkCookie both compare against the admin token using
// crypto/subtle.ConstantTimeCompare. A naive == on []byte / string would
// short-circuit at the first mismatched byte: the request-handling cost is
// tiny compared to round-trip latency, but an attacker running from the same
// host (or close to it on localhost) can measure those nanoseconds across
// many requests and recover the admin token byte-by-byte. The admin token
// grants full dashboard access (agents, secrets, audit), so a timing oracle
// here is a full-compromise primitive. subtle.ConstantTimeCompare walks
// both operands in constant time when they have equal length (and
// short-circuits to 0 on length mismatch); an attacker probing a token of
// known length — which ours is, at 64 hex chars — sees constant-time
// behaviour, which is what closes the byte-by-byte oracle.
func checkBearer(r *http.Request, token []byte) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	provided := []byte(strings.TrimPrefix(auth, "Bearer "))
	return subtle.ConstantTimeCompare(provided, token) == 1
}

func checkCookie(r *http.Request, token []byte) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), token) == 1
}
