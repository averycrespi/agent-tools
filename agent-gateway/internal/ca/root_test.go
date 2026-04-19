package ca_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/ca"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrGenerate_CreatesOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.pem")

	a, err := ca.LoadOrGenerate(keyPath, certPath)
	require.NoError(t, err)

	// Check file perms.
	st, _ := os.Stat(keyPath)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	st, _ = os.Stat(certPath)
	assert.Equal(t, os.FileMode(0o644), st.Mode().Perm())

	// Cert is self-signed, 10-year validity, P-256 ECDSA.
	block, _ := pem.Decode(a.RootPEM())
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "agent-gateway local CA", cert.Subject.CommonName)
	assert.True(t, cert.IsCA)
	assert.InDelta(t, 10*365*24, cert.NotAfter.Sub(cert.NotBefore).Hours(), 48)
}

func TestLoadOrGenerate_Reuses(t *testing.T) {
	dir := t.TempDir()
	a1, _ := ca.LoadOrGenerate(filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.pem"))
	a2, _ := ca.LoadOrGenerate(filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.pem"))
	assert.Equal(t, a1.RootPEM(), a2.RootPEM())
}

func TestLoadOrGenerate_CertProperties(t *testing.T) {
	dir := t.TempDir()
	a, err := ca.LoadOrGenerate(filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.pem"))
	require.NoError(t, err)

	block, _ := pem.Decode(a.RootPEM())
	require.NotNil(t, block)
	assert.Equal(t, "CERTIFICATE", block.Type)

	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	assert.True(t, cert.BasicConstraintsValid)
	assert.Equal(t, x509.KeyUsageCertSign|x509.KeyUsageCRLSign, cert.KeyUsage)
	assert.Equal(t, x509.ECDSAWithSHA256, cert.SignatureAlgorithm)
}

func TestLoadOrGenerate_KeyPEMType(t *testing.T) {
	dir := t.TempDir()
	_, err := ca.LoadOrGenerate(filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.pem"))
	require.NoError(t, err)

	keyBytes, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	require.NoError(t, err)

	block, _ := pem.Decode(keyBytes)
	require.NotNil(t, block)
	assert.Equal(t, "EC PRIVATE KEY", block.Type)
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.pem")

	a1, err := ca.LoadOrGenerate(keyPath, certPath)
	require.NoError(t, err)
	origPEM := append([]byte(nil), a1.RootPEM()...)

	err = a1.Rotate()
	require.NoError(t, err)

	// Rotate produces different PEM bytes.
	assert.NotEqual(t, origPEM, a1.RootPEM())

	// New files can be loaded and produce the same PEM as the rotated authority.
	a2, err := ca.LoadOrGenerate(keyPath, certPath)
	require.NoError(t, err)
	assert.Equal(t, a1.RootPEM(), a2.RootPEM())
}

// TestRotate_ClearsLeafCache verifies that after Rotate, an existing host's
// leaf is re-issued under the new root rather than served from the cache.
func TestRotate_ClearsLeafCache(t *testing.T) {
	dir := t.TempDir()
	a, err := ca.LoadOrGenerate(filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.pem"))
	require.NoError(t, err)

	cfgBefore, err := a.ServerConfig("example.test")
	require.NoError(t, err)
	_, present := ca.CacheLookupForTest(a, "example.test")
	require.True(t, present, "expected leaf to be cached after first ServerConfig")

	require.NoError(t, a.Rotate())

	_, present = ca.CacheLookupForTest(a, "example.test")
	assert.False(t, present, "Rotate must clear the leaf cache")

	cfgAfter, err := a.ServerConfig("example.test")
	require.NoError(t, err)
	assert.NotSame(t, cfgBefore, cfgAfter,
		"after rotation the next ServerConfig must return a freshly issued leaf")
}

// TestReload_PicksUpDiskChanges verifies that Reload re-reads the on-disk CA
// files (typically written by a sibling `ca rotate` CLI process) and clears
// the leaf cache so subsequent leaves are signed under the new root.
func TestReload_PicksUpDiskChanges(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.pem")

	// Daemon-side authority: the long-lived in-memory copy.
	daemon, err := ca.LoadOrGenerate(keyPath, certPath)
	require.NoError(t, err)
	origPEM := append([]byte(nil), daemon.RootPEM()...)

	// Warm the leaf cache so Reload has something to drop.
	_, err = daemon.ServerConfig("example.test")
	require.NoError(t, err)
	_, present := ca.CacheLookupForTest(daemon, "example.test")
	require.True(t, present)

	// CLI-side: rotate using a fresh Authority that points at the same files.
	cli, err := ca.LoadOrGenerate(keyPath, certPath)
	require.NoError(t, err)
	require.NoError(t, cli.Rotate())

	// Daemon's in-memory copy still holds the old root until Reload.
	assert.Equal(t, origPEM, daemon.RootPEM(),
		"daemon must not see new root until Reload is called")

	require.NoError(t, daemon.Reload())

	assert.NotEqual(t, origPEM, daemon.RootPEM(),
		"after Reload the daemon must serve the new root PEM")
	assert.Equal(t, cli.RootPEM(), daemon.RootPEM(),
		"daemon and CLI must agree on the rotated root")
	_, present = ca.CacheLookupForTest(daemon, "example.test")
	assert.False(t, present, "Reload must clear the leaf cache")
}

// TestReload_ConcurrentWithIssuance is a race-detector test that exercises
// concurrent ServerConfig calls (which read the root bundle on cache miss) and
// Reload calls (which swap it). With the atomic.Pointer in place no data race
// should be reported by `go test -race`.
func TestReload_ConcurrentWithIssuance(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.pem")

	a, err := ca.LoadOrGenerate(keyPath, certPath)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			// Each call to a unique host forces a cache miss, exercising
			// the bundle read path inside issueLeaf.
			host := "h" + string(rune('a'+(i%26))) + ".test"
			_, _ = a.ServerConfig(host)
		}
	}()

	for i := 0; i < 5; i++ {
		require.NoError(t, a.Reload())
	}
	<-done
}
