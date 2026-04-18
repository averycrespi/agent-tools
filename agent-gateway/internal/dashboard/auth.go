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
	"time"
)

const cookieName = "agent-gateway-auth"

// EnsureAdminToken loads the admin token from path, or generates and writes a
// new one if the file does not exist. Returns the 64-character hex token.
func EnsureAdminToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading admin token file: %w", err)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating admin token: %w", err)
	}
	token := hex.EncodeToString(b)

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("writing admin token file: %w", err)
	}
	return token, nil
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
func authMiddleware(token string, next http.Handler) http.Handler {
	tokenBytes := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Allowlisted: /ca.pem and /dashboard/unauthorized.
		if path == "/ca.pem" || path == "/dashboard/unauthorized" {
			next.ServeHTTP(w, r)
			return
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
