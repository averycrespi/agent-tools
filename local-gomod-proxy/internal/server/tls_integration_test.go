//go:build integration

package server

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/auth"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_TLSAndAuth spins up the full handler behind TLS and asserts
// that auth is required and honored.
func TestIntegration_TLSAndAuth(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := state.LoadOrGenerateCert(dir)
	require.NoError(t, err)
	creds, err := state.LoadOrGenerateCredentials(dir)
	require.NoError(t, err)

	// Upstream stub: if the request reaches it, we returned 200.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	handler := auth.Middleware(
		New(
			router.New("github.com/never-match/*"),
			private.New(&stubRunner{}),
			public.New(u),
			8,
		),
		creds,
	)

	// Start a TLS server using the generated cert.
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	certPEM, err := os.ReadFile(certPath)
	require.NoError(t, err)
	require.True(t, pool.AppendCertsFromPEM(certPEM))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	t.Run("no creds → 401", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/rsc.io/quote/@v/list", nil)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Equal(t, `Basic realm="local-gomod-proxy"`, resp.Header.Get("WWW-Authenticate"))
	})

	t.Run("wrong creds → 401", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/rsc.io/quote/@v/list", nil)
		req.SetBasicAuth("x", "wrong")
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("correct creds → 200", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/rsc.io/quote/@v/list", nil)
		req.SetBasicAuth(creds.Username, creds.Password)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "upstream-ok", string(body))
	})
}
