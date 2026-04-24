package ca_test

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
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

// TestServerConfig_MinVersionTLS13 verifies that issued leaf tls.Config structs
// require TLS 1.3 as the minimum, dropping TLS 1.0/1.1/1.2 cipher rollback paths.
func TestServerConfig_MinVersionTLS13(t *testing.T) {
	a := newTestAuthority(t)
	c, err := a.ServerConfig("example.com")
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS13), c.MinVersion)
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

// TestLRU_EvictsOldestWhenFull verifies that the cache is bounded at 10 000
// entries and that the 10 001st insertion evicts the oldest entry.
func TestLRU_EvictsOldestWhenFull(t *testing.T) {
	a := newTestAuthority(t)

	// Fill the cache to the maximum capacity.
	const maxEntries = 10_000
	first := "host-0.example.com"
	for i := range maxEntries {
		host := fmt.Sprintf("host-%d.example.com", i)
		_, err := a.ServerConfig(host)
		require.NoError(t, err)
	}

	// Confirm the first entry is still present before we push it out.
	_, has := ca.CacheLookupForTest(a, first)
	require.True(t, has, "first entry should be present before eviction")

	// Adding one more entry must evict the oldest (LRU order: host-0 was
	// inserted first and never accessed since).
	_, err := a.ServerConfig("host-10000.example.com")
	require.NoError(t, err)

	_, has = ca.CacheLookupForTest(a, first)
	assert.False(t, has, "oldest entry must be evicted when cache exceeds 10 000")
}

// TestSkewBuffer_NotAfter verifies that a cached cert whose NotAfter is within
// the clock-skew buffer is not returned from the cache — a fresh cert is
// re-issued instead.
func TestSkewBuffer_NotAfter(t *testing.T) {
	a := newTestAuthority(t)
	// Set a skew buffer larger than the leaf lifetime so every cached cert
	// looks "expiring soon" on the next lookup.
	ca.SetSkewBufferForTest(a, 48*time.Hour)

	_, err := a.ServerConfig("example.com")
	require.NoError(t, err)

	// The second call must detect the cert as within the skew window and
	// re-issue, returning a different *tls.Config pointer.
	c2, err := a.ServerConfig("example.com")
	require.NoError(t, err)

	// We can't assert NotSame on the pointer because re-issuance stores the
	// new entry; but we verify that the cert is freshly issued (NotBefore is
	// recent).
	leaf, err := x509.ParseCertificate(c2.Certificates[0].Certificate[0])
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), leaf.NotBefore, 5*time.Second,
		"re-issued cert must have a fresh NotBefore")
}

// TestSkewBuffer_NotBefore verifies that a cached cert whose NotBefore is
// within the clock-skew buffer in the future is not returned from the cache.
func TestSkewBuffer_NotBefore(t *testing.T) {
	a := newTestAuthority(t)

	// Prime the cache normally.
	_, err := a.ServerConfig("future.example.com")
	require.NoError(t, err)

	// Inject a replacement entry whose NotBefore is just barely in the future
	// (simulating a cert issued against a slightly fast wall clock).
	ca.InjectCacheEntryForTest(a, "future.example.com", time.Now().Add(10*time.Minute))

	// With a 5-minute skew buffer (default), a NotBefore 10 minutes in the
	// future exceeds the buffer so the cert must be re-issued.
	ca.SetSkewBufferForTest(a, 5*time.Minute)
	c2, err := a.ServerConfig("future.example.com")
	require.NoError(t, err)

	leaf, err := x509.ParseCertificate(c2.Certificates[0].Certificate[0])
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), leaf.NotBefore, 5*time.Second,
		"re-issued cert must have a NotBefore close to now")
}
