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

// TokenPath returns the default token file path under the XDG config directory.
func TokenPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "mcp-broker", "auth-token")
}

// EnsureToken loads the token from path, or generates and writes a new one if the file doesn't exist.
// Returns the 64-character hex token string.
func EnsureToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}

	// Generate new token.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := hex.EncodeToString(b)

	// Write to file.
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("writing token file: %w", err)
	}
	return token, nil
}

// LoadToken reads the token from path. Returns an error if the file doesn't exist.
func LoadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return string(data), nil
}

const cookieName = "mcp-broker-auth"

// Middleware returns an HTTP handler that checks every request for a valid auth token.
func Middleware(token string, next http.Handler) http.Handler {
	tokenBytes := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// 1. /dashboard/unauthorized is always allowed.
		if path == "/dashboard/unauthorized" {
			next.ServeHTTP(w, r)
			return
		}

		isDashboard := strings.HasPrefix(path, "/dashboard")

		// 2. Dashboard with ?token= query param: set cookie and redirect before serving.
		if isDashboard {
			if qToken := r.URL.Query().Get("token"); qToken != "" {
				if subtle.ConstantTimeCompare([]byte(qToken), tokenBytes) == 1 {
					//nolint:gosec // Dashboard is loopback-only plain HTTP; Secure cookies would prevent local auth from working.
					http.SetCookie(w, &http.Cookie{
						Name:     cookieName,
						Value:    token,
						Path:     "/dashboard/",
						HttpOnly: true,
						SameSite: http.SameSiteStrictMode,
						MaxAge:   int(365 * 24 * time.Hour / time.Second),
					})
					redirectWithoutToken(w, r)
					return
				}
			}
		}

		// 3. Check Authorization: Bearer <token> header.
		if checkBearer(r, tokenBytes) {
			next.ServeHTTP(w, r)
			return
		}

		// 4. Check cookie.
		if checkCookie(r, tokenBytes) {
			next.ServeHTTP(w, r)
			return
		}

		if isDashboard {
			// 5. Dashboard, no valid auth: redirect to unauthorized page.
			http.Redirect(w, r, "/dashboard/unauthorized", http.StatusFound)
			return
		}

		// 6. Non-dashboard (i.e., /mcp): 401.
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
}

func redirectWithoutToken(w http.ResponseWriter, r *http.Request) {
	clean := *r.URL
	q := clean.Query()
	q.Del("token")
	clean.RawQuery = q.Encode()
	//nolint:gosec // RequestURI is a relative same-origin redirect, not an external target.
	http.Redirect(w, r, clean.RequestURI(), http.StatusFound)
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
