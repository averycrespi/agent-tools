package dashboard

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// cookieSecureCases enumerates the four (r.TLS, X-Forwarded-Proto) permutations
// and the Secure attribute each should produce.
//
// Both cookie-issuing sites (query-token exchange in authMiddleware and the
// unauthorized POST handler) must respect this truth table. Covering both
// handlers with the full matrix guards against a copy-paste drift where only
// one site gets the helper call.
var cookieSecureCases = []struct {
	name       string
	tls        bool
	xfp        string
	wantSecure bool
}{
	{name: "plain_http_no_xfp", tls: false, xfp: "", wantSecure: false},
	{name: "plain_http_xfp_https", tls: false, xfp: "https", wantSecure: true},
	{name: "tls_no_xfp", tls: true, xfp: "", wantSecure: true},
	{name: "tls_and_xfp_https", tls: true, xfp: "https", wantSecure: true},
}

// findAuthCookie returns the Set-Cookie entry for the dashboard auth cookie
// from a response recorder, or nil if not present.
func findAuthCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

// TestCookieSecure_Helper exercises the helper directly for the four
// (r.TLS, X-Forwarded-Proto) permutations.
func TestCookieSecure_Helper(t *testing.T) {
	for _, tc := range cookieSecureCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			require.Equal(t, tc.wantSecure, cookieSecure(req))
		})
	}
}

// TestCookieSecure_QueryTokenExchange drives the ?token= query-param exchange
// path in authMiddleware and asserts Set-Cookie Secure reflects the request
// scheme.
func TestCookieSecure_QueryTokenExchange(t *testing.T) {
	token := strings.Repeat("a", 64)
	var tokenPtr atomic.Pointer[string]
	tokenPtr.Store(&token)

	// Inner handler is never reached on the query-token path — middleware
	// redirects first. A no-op is sufficient.
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
	h := authMiddleware(&tokenPtr, inner)

	for _, tc := range cookieSecureCases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/dashboard/?token=" + url.QueryEscape(token)
			req := httptest.NewRequest("GET", target, nil)
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, http.StatusFound, rr.Code)

			c := findAuthCookie(rr.Result())
			require.NotNil(t, c, "expected auth cookie to be set")
			require.Equal(t, tc.wantSecure, c.Secure)
		})
	}
}

// TestCookieSecure_UnauthorizedPOST drives handleUnauthorizedPOST with a
// valid token and asserts Set-Cookie Secure reflects the request scheme.
func TestCookieSecure_UnauthorizedPOST(t *testing.T) {
	// Pre-write an admin-token file so Handler() loads it into tokenPtr.
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "admin-token")
	token := strings.Repeat("b", 64)
	require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))

	s := New(Deps{AdminTokenPath: tokenPath})
	h := s.Handler()

	for _, tc := range cookieSecureCases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("token", token)
			req := httptest.NewRequest("POST", "/dashboard/unauthorized",
				strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, http.StatusFound, rr.Code)

			c := findAuthCookie(rr.Result())
			require.NotNil(t, c, "expected auth cookie to be set")
			require.Equal(t, tc.wantSecure, c.Secure)
		})
	}
}
