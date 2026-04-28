package state

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrGenerateCert_GeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "cert.pem"), certPath)
	assert.Equal(t, filepath.Join(dir, "key.pem"), keyPath)

	certInfo, err := os.Stat(certPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), certInfo.Mode().Perm())

	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())

	cert := parseCertFile(t, certPath)
	assert.Equal(t, "local-gomod-proxy", cert.Subject.CommonName)
	assert.ElementsMatch(t, []string{"localhost", "host.lima.internal"}, cert.DNSNames)
	assert.Contains(t, cert.IPAddresses, net.ParseIP("127.0.0.1").To4())
	// Validity window: ~1 year.
	assert.WithinDuration(t, time.Now().Add(365*24*time.Hour), cert.NotAfter, 24*time.Hour)
}

func TestLoadOrGenerateCert_ReusesWhenFresh(t *testing.T) {
	dir := t.TempDir()
	cert1, key1, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)
	certBytes1, _ := os.ReadFile(cert1)

	cert2, key2, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)
	certBytes2, _ := os.ReadFile(cert2)

	assert.Equal(t, cert1, cert2)
	assert.Equal(t, key1, key2)
	assert.Equal(t, certBytes1, certBytes2, "cert must not be regenerated when fresh")
}

func TestLoadOrGenerateCert_RegeneratesWhenNearExpiry(t *testing.T) {
	dir := t.TempDir()
	// Seed a cert that expires in 10 days — inside the 30-day renewal window.
	writeTestCert(t, dir, 10*24*time.Hour)
	certBytes1, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))

	_, _, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)

	certBytes2, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))
	assert.NotEqual(t, certBytes1, certBytes2, "cert should have been regenerated")

	cert := parseCertFile(t, filepath.Join(dir, "cert.pem"))
	assert.WithinDuration(t, time.Now().Add(365*24*time.Hour), cert.NotAfter, 24*time.Hour)
}

func TestLoadOrGenerateCert_RegeneratesWhenUnparseable(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("garbage"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "key.pem"), []byte("garbage"), 0o600))

	_, _, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)

	cert := parseCertFile(t, filepath.Join(dir, "cert.pem"))
	assert.Equal(t, "local-gomod-proxy", cert.Subject.CommonName)
}

func parseCertFile(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	block, _ := pem.Decode(raw)
	require.NotNil(t, block, "expected PEM block")
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

// writeTestCert writes a self-signed cert/key pair with the given remaining
// validity, used to seed regen-path tests.
func writeTestCert(t *testing.T, dir string, validFor time.Duration) {
	t.Helper()
	_, _, err := generateCert(dir, validFor)
	require.NoError(t, err)
}
