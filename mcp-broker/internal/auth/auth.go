package auth

import (
	"crypto/subtle"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TokenPath returns the legacy migration token path under the XDG config directory.
func TokenPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "mcp-broker", "auth-token")
}

const cookieName = "mcp-broker-auth"

// Middleware enforces the exact role assigned to each broker route.
func Middleware(store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/healthz" || path == "/dashboard/unauthorized" {
			next.ServeHTTP(w, r)
			return
		}

		tokens := store.Snapshot()
		if path == "/mcp" {
			if checkBearer(r, []byte(tokens.Agent)) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		isDashboard := path == "/dashboard" || strings.HasPrefix(path, "/dashboard/")
		adminToken := []byte(tokens.Admin)
		if isDashboard {
			if queryToken := r.URL.Query().Get("token"); queryToken != "" && constantTimeEqual(queryToken, adminToken) {
				//nolint:gosec // Dashboard is loopback-only plain HTTP; Secure cookies would prevent local auth from working.
				http.SetCookie(w, &http.Cookie{
					Name:     cookieName,
					Value:    tokens.Admin,
					Path:     "/dashboard/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
					MaxAge:   int(365 * 24 * time.Hour / time.Second),
				})
				redirectWithoutToken(w, r)
				return
			}
		}

		if checkBearer(r, adminToken) || (isDashboard && checkCookie(r, adminToken)) {
			next.ServeHTTP(w, r)
			return
		}
		if isDashboard {
			http.Redirect(w, r, "/dashboard/unauthorized", http.StatusFound)
			return
		}
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
}

func redirectWithoutToken(w http.ResponseWriter, r *http.Request) {
	clean := *r.URL
	query := clean.Query()
	query.Del("token")
	clean.RawQuery = query.Encode()
	//nolint:gosec // RequestURI is a relative same-origin redirect, not an external target.
	http.Redirect(w, r, clean.RequestURI(), http.StatusFound)
}

func checkBearer(r *http.Request, token []byte) bool {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return false
	}
	return constantTimeEqual(strings.TrimPrefix(authorization, "Bearer "), token)
}

func checkCookie(r *http.Request, token []byte) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return constantTimeEqual(cookie.Value, token)
}

func constantTimeEqual(provided string, expected []byte) bool {
	return len(expected) > 0 && subtle.ConstantTimeCompare([]byte(provided), expected) == 1
}
