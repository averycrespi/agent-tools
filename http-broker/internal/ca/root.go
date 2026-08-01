// Package ca manages the local root certificate authority http-broker uses
// to sign TLS certificates for intercepted connections.
//
// Ported from the agent-gateway branch of this repository
// (origin/agent-gateway, internal/ca), keeping its LRU bound, its clock-skew
// buffer, and its IP-literal SAN branch. Changes: the root is loaded and
// generated through one code path that validates what it read, and Rotate
// refuses to run against a directory it cannot write.
//
// The root's private key never leaves the host. Sandboxes receive only ca.pem,
// shipped in by sandbox-manager's copy_paths.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/http-broker/internal/atomicfile"
)

// rootLifetime is how long a generated root CA is valid. Rotation invalidates
// every provisioned sandbox at once, so the lifetime is long and rotation is
// a deliberate, documented operation rather than routine maintenance.
const rootLifetime = 10 * 365 * 24 * time.Hour

// File modes: the key is secret, the certificate is meant to be copied into
// sandboxes and read by anything that needs to trust it.
const (
	keyMode  os.FileMode = 0o600
	certMode os.FileMode = 0o644
)

// rootBundle holds the rotatable cert/key/PEM triple. It sits behind an
// atomic.Pointer so concurrent leaf issuance never observes a torn state and
// rotation can swap it without locking the issuance path.
type rootBundle struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	rootPEM []byte
}

// Authority holds a loaded or freshly generated root CA together with a cache
// of issued leaf TLS configs.
type Authority struct {
	current  atomic.Pointer[rootBundle]
	keyPath  string
	certPath string

	cache         *leafCache
	leafLifetime  time.Duration
	sweepBuffer   time.Duration
	sweepInterval time.Duration
	skewBuffer    time.Duration
}

// RootPEM returns the PEM-encoded certificate for the root CA. This is what
// GET /ca.pem serves and what provisioning installs into a sandbox.
func (a *Authority) RootPEM() []byte { return a.current.Load().rootPEM }

// RootCert returns the parsed root certificate.
func (a *Authority) RootCert() *x509.Certificate { return a.current.Load().cert }

// LoadOrGenerate loads an existing CA when both files are present, or
// generates a new P-256 ECDSA root and writes it.
func LoadOrGenerate(keyPath, certPath string) (*Authority, error) {
	if fileExists(keyPath) && fileExists(certPath) {
		return load(keyPath, certPath)
	}
	// A half-present pair means a previous write was interrupted or a file was
	// deleted by hand. Regenerating is right, but say so rather than silently
	// invalidating every sandbox that trusts the surviving certificate.
	if fileExists(certPath) != fileExists(keyPath) {
		return nil, fmt.Errorf(
			"ca: found only one of %s and %s; remove the remaining file to regenerate the CA, then re-run provisioning in every sandbox",
			keyPath, certPath)
	}
	return generate(keyPath, certPath)
}

// Rotate generates a new root CA, atomically replaces the files on disk, swaps
// the in-memory bundle, and clears the leaf cache so later handshakes use
// leaves signed by the new root.
//
// In-flight handshakes holding an old leaf complete normally. Every
// provisioned sandbox stops trusting this proxy until provisioning is re-run
// there; there is no overlap window.
func (a *Authority) Rotate() error {
	next, err := generate(a.keyPath, a.certPath)
	if err != nil {
		return err
	}
	a.current.Store(next.current.Load())
	a.clearLeafCache()
	return nil
}

// Reload re-reads the CA files and swaps them in, picking up a `ca rotate`
// performed by a separate CLI process. On failure the previous bundle stays
// live and the error is returned for the caller to log.
func (a *Authority) Reload() error {
	next, err := load(a.keyPath, a.certPath)
	if err != nil {
		return err
	}
	a.current.Store(next.current.Load())
	a.clearLeafCache()
	return nil
}

func (a *Authority) clearLeafCache() {
	if a.cache != nil {
		a.cache.purge()
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func load(keyPath, certPath string) (*Authority, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read key: %w", err)
	}
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(keyBytes)
	if keyBlock == nil {
		return nil, fmt.Errorf("ca: %s does not contain a PEM block", keyPath)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse key: %w", err)
	}

	certBlock, _ := pem.Decode(certBytes)
	if certBlock == nil {
		return nil, fmt.Errorf("ca: %s does not contain a PEM block", certPath)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse certificate: %w", err)
	}

	// A cert that is not a CA, or whose key does not match, cannot sign
	// leaves. Failing here beats failing opaquely inside a TLS handshake.
	if !cert.IsCA {
		return nil, fmt.Errorf("ca: %s is not a CA certificate", certPath)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("ca: certificate and key do not match")
	}

	a := &Authority{keyPath: keyPath, certPath: certPath}
	a.current.Store(&rootBundle{cert: cert, key: key, rootPEM: certBytes})
	initLeafFields(a)
	return a, nil
}

func generate(keyPath, certPath string) (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "http-broker local CA"},
		NotBefore:             now.Add(-skewAllowance),
		NotAfter:              now.Add(rootLifetime),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		// MaxPathLen 0 with MaxPathLenZero prevents anything this root signs
		// from itself acting as an intermediate CA. BasicConstraintsValid
		// alone encodes the CA flag but does not constrain path length, so a
		// leaf could otherwise be pressed into signing further certificates.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca: create certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse generated certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ca: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	// Key first: a certificate on disk with no matching key is the confusing
	// half-state LoadOrGenerate has to diagnose.
	if err := atomicfile.Write(keyPath, keyPEM, keyMode); err != nil {
		return nil, fmt.Errorf("ca: write key: %w", err)
	}
	if err := atomicfile.Write(certPath, certPEM, certMode); err != nil {
		return nil, fmt.Errorf("ca: write certificate: %w", err)
	}

	a := &Authority{keyPath: keyPath, certPath: certPath}
	a.current.Store(&rootBundle{cert: cert, key: key, rootPEM: certPEM})
	initLeafFields(a)
	return a, nil
}

// randomSerial returns a random 128-bit serial number.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("ca: generate serial: %w", err)
	}
	return serial, nil
}
