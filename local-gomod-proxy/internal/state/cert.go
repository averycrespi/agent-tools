package state

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certFile      = "cert.pem"
	keyFile       = "key.pem"
	certValidity  = 365 * 24 * time.Hour
	renewalWindow = 30 * 24 * time.Hour
)

// LoadOrGenerateCert returns paths to the TLS cert and key in dir. If both
// files exist, parse cleanly, and the cert has more than renewalWindow left
// before expiry, the existing pair is reused. Otherwise a fresh ECDSA P-256
// self-signed cert is generated and written in-place.
func LoadOrGenerateCert(dir string) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dir, certFile)
	keyPath = filepath.Join(dir, keyFile)

	if reusable(certPath, keyPath) {
		return certPath, keyPath, nil
	}
	slog.Info("generating new TLS cert", "dir", dir, "validity", certValidity)
	return generateCert(dir, certValidity)
}

func reusable(certPath, keyPath string) bool {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	if _, err := os.Stat(keyPath); err != nil {
		return false
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	return time.Until(cert.NotAfter) > renewalWindow
}

func generateCert(dir string, validFor time.Duration) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dir, certFile)
	keyPath = filepath.Join(dir, keyFile)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating ECDSA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("generating serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "local-gomod-proxy"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost", "host.lima.internal"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("creating certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", "", fmt.Errorf("writing cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshaling key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("writing key: %w", err)
	}

	return certPath, keyPath, nil
}
