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
