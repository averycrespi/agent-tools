// Package ca manages the local root certificate authority used by agent-gateway
// to sign TLS certificates for intercepted connections.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"time"
)

// Authority holds a loaded or freshly-generated root CA certificate and key.
type Authority struct {
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	rootPEM  []byte
	keyPath  string
	certPath string
}

// RootPEM returns the PEM-encoded DER certificate for the root CA.
func (a *Authority) RootPEM() []byte {
	return a.rootPEM
}

// LoadOrGenerate loads an existing CA from keyPath / certPath when both files
// are present, or generates a new P-256 ECDSA root CA and writes it to those
// paths.  Key is written with mode 0600; cert with mode 0644.
func LoadOrGenerate(keyPath, certPath string) (*Authority, error) {
	keyExists := fileExists(keyPath)
	certExists := fileExists(certPath)

	if keyExists && certExists {
		return load(keyPath, certPath)
	}

	return generate(keyPath, certPath)
}

// Rotate generates a new root CA, atomically replaces the on-disk files, and
// updates the Authority in place.
func (a *Authority) Rotate() error {
	next, err := generate(a.keyPath, a.certPath)
	if err != nil {
		return err
	}
	a.cert = next.cert
	a.key = next.key
	a.rootPEM = next.rootPEM
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func load(keyPath, certPath string) (*Authority, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}

	keyBlock, _ := pem.Decode(keyBytes)
	if keyBlock == nil {
		return nil, errors.New("ca: failed to decode key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}

	certBlock, _ := pem.Decode(certBytes)
	if certBlock == nil {
		return nil, errors.New("ca: failed to decode cert PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}

	return &Authority{
		cert:     cert,
		key:      key,
		rootPEM:  certBytes,
		keyPath:  keyPath,
		certPath: certPath,
	}, nil
}

func generate(keyPath, certPath string) (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "agent-gateway local CA",
		},
		NotBefore:             now,
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, err
	}

	// PEM-encode key and cert.
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	// Atomic write: write to .new files then rename into place.
	if err := writeAtomic(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeAtomic(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}

	return &Authority{
		cert:     cert,
		key:      key,
		rootPEM:  certPEM,
		keyPath:  keyPath,
		certPath: certPath,
	}, nil
}

// randomSerial returns a random 128-bit serial number suitable for x509.
func randomSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, max)
}

// writeAtomic writes data to path.new then renames it into place with perm.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
