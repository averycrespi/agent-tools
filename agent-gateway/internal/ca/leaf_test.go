package ca_test

import (
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/ca"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestAuthority creates an Authority with a freshly generated root CA using
// t.TempDir for the on-disk files.
func newTestAuthority(t *testing.T) *ca.Authority {
	t.Helper()
	dir := t.TempDir()
	a, err := ca.LoadOrGenerate(
		filepath.Join(dir, "ca.key"),
		filepath.Join(dir, "ca.pem"),
	)
	require.NoError(t, err)
	return a
}

// newTestAuthorityWithBuffer creates an Authority with a configurable leaf
// lifetime (so the sweeper test can issue very short-lived certs).
func newTestAuthorityWithBuffer(t *testing.T, leafLifetime time.Duration) *ca.Authority {
	t.Helper()
	a := newTestAuthority(t)
	ca.SetLeafLifetimeForTest(a, leafLifetime)
	return a
}

func TestServerConfig_CacheHitReusesConfig(t *testing.T) {
	a := newTestAuthority(t)
	c1, err := a.ServerConfig("example.com")
	require.NoError(t, err)
	c2, err := a.ServerConfig("example.com")
	require.NoError(t, err)
	assert.Same(t, c1, c2)
}

func TestServerConfig_NormalizesCacheKey(t *testing.T) {
	a := newTestAuthority(t)
	c1, err := a.ServerConfig("API.GitHub.COM.")
	require.NoError(t, err)
	c2, err := a.ServerConfig("api.github.com")
	require.NoError(t, err)
	assert.Same(t, c1, c2, "case/trailing-dot variants must share one cache entry")
}

func TestServerConfig_DifferentHostsDifferentCerts(t *testing.T) {
	a := newTestAuthority(t)
	c1, err := a.ServerConfig("a.example.com")
	require.NoError(t, err)
	c2, err := a.ServerConfig("b.example.com")
	require.NoError(t, err)
	assert.NotSame(t, c1, c2)
	assert.Equal(t, []string{"h2", "http/1.1"}, c1.NextProtos)
}

func TestServerConfig_LeafVerifiesAgainstRoot(t *testing.T) {
	a := newTestAuthority(t)
	c, err := a.ServerConfig("example.com")
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(a.RootPEM())

	leaf, err := x509.ParseCertificate(c.Certificates[0].Certificate[0])
	require.NoError(t, err)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "example.com"})
	require.NoError(t, err)
}

func TestServerConfig_MinVersionTLS12(t *testing.T) {
	a := newTestAuthority(t)
	c, err := a.ServerConfig("example.com")
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS12), c.MinVersion)
}

func TestServerConfig_IPLiteralUsesIPAddresses(t *testing.T) {
	a := newTestAuthority(t)
	c, err := a.ServerConfig("192.168.1.1")
	require.NoError(t, err)

	leaf, err := x509.ParseCertificate(c.Certificates[0].Certificate[0])
	require.NoError(t, err)
	assert.Len(t, leaf.IPAddresses, 1)
	assert.Equal(t, "192.168.1.1", leaf.IPAddresses[0].String())
	assert.Empty(t, leaf.DNSNames)
}

func TestSweeper_RemovesExpired(t *testing.T) {
	// Use a very short leaf lifetime so the cert expires almost immediately.
	a := newTestAuthorityWithBuffer(t, 1*time.Millisecond)
	_, _ = a.ServerConfig("example.com")
	time.Sleep(20 * time.Millisecond)
	ca.SweepOnceForTest(a)
	_, has := ca.CacheLookupForTest(a, "example.com")
	assert.False(t, has)
}
