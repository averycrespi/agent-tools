package dashboard

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSecureHeaders_AppliedToAPIResponse asserts the security header middleware
// stamps the CSP + hardening headers on authenticated API responses. If any of
// these drop, XSS / clickjacking / MIME-sniffing defences regress silently —
// assertions here are the canary.
func TestSecureHeaders_AppliedToAPIResponse(t *testing.T) {
	srv, token := newTestServer(t, Deps{})

	req, _ := http.NewRequest("GET", srv.URL+"/dashboard/api/pending", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	wantCSP := "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'"
	require.Equal(t, wantCSP, resp.Header.Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
}

// TestSecureHeaders_AppliedToDashboardIndex asserts the CSP is stamped on the
// HTML response, not just JSON — the index is the main XSS attack surface, so
// this is the most important route to protect.
func TestSecureHeaders_AppliedToDashboardIndex(t *testing.T) {
	srv, token := newTestServer(t, Deps{})

	req, _ := http.NewRequest("GET", srv.URL+"/dashboard/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Contains(t, resp.Header.Get("Content-Security-Policy"), "script-src 'self'")
	require.Contains(t, resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
}

// TestSecureHeaders_CAPemSkipsCSP asserts /ca.pem does NOT carry a CSP header
// (per design: CSP on a cert download is nonsensical) but DOES keep the other
// hardening headers. Its Content-Type must remain application/x-pem-file so
// browsers hand the file to the OS cert installer rather than rendering it.
func TestSecureHeaders_CAPemSkipsCSP(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, []byte("FAKE CA"), 0o644))

	srv, _ := newTestServer(t, Deps{CAPath: caPath})

	resp, err := http.Get(srv.URL + "/ca.pem")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// No CSP on the cert download.
	require.Empty(t, resp.Header.Get("Content-Security-Policy"))

	// Other hardening headers still apply.
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))

	// Content-Type for the CA cert is preserved.
	require.Equal(t, "application/x-pem-file", resp.Header.Get("Content-Type"))

	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "FAKE CA", string(body))
}

// TestSecureHeaders_UnauthorizedPageCarriesCSP asserts the /dashboard/unauthorized
// form (rendered inline HTML) is CSP-protected — this page takes a password
// input, so missing CSP here would be particularly bad.
func TestSecureHeaders_UnauthorizedPageCarriesCSP(t *testing.T) {
	srv, _ := newTestServer(t, Deps{})

	resp, err := http.Get(srv.URL + "/dashboard/unauthorized")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.NotEmpty(t, resp.Header.Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
}
