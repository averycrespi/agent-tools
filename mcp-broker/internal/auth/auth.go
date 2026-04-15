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

	"github.com/averycrespi/agent-tools/mcp-broker/internal/grants"
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

		// 2. Check Authorization: Bearer <token> header.
		if checkBearer(r, tokenBytes) {
			next.ServeHTTP(w, r)
			return
		}

		// 3. Check cookie.
		if checkCookie(r, tokenBytes) {
			next.ServeHTTP(w, r)
			return
		}

		isDashboard := strings.HasPrefix(path, "/dashboard")

		// 4. Dashboard with ?token= query param: set cookie and redirect.
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
					})
					// Redirect to path without the token query param.
					clean := *r.URL
					q := clean.Query()
					q.Del("token")
					clean.RawQuery = q.Encode()
					http.Redirect(w, r, clean.RequestURI(), http.StatusFound)
					return
				}
			}

			// 5. Dashboard, no valid auth: redirect to unauthorized page.
			http.Redirect(w, r, "/dashboard/unauthorized", http.StatusFound)
			return
		}

		// 6. Non-dashboard (i.e., /mcp): 401.
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

// GrantTokenMiddleware reads X-Grant-Token from the request and, if
// present, attaches it to the request context via grants.ContextWithToken.
// Absence of the header is not an error: downstream code treats an empty
// token as "no grant presented."
func GrantTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := r.Header.Get("X-Grant-Token"); t != "" {
			r = r.WithContext(grants.ContextWithToken(r.Context(), t))
		}
		next.ServeHTTP(w, r)
	})
}
