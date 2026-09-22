// Package httpca owns protected installation CA material and bounded issuance.
package httpca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/netip"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

var ErrUnavailable = errors.New("interception CA unavailable")

type Clock interface{ Now() time.Time }

type envelope struct {
	Version      int    `json:"version"`
	Installation string `json:"installation"`
	Key          []byte `json:"key"`
	Certificate  []byte `json:"certificate"`
}

// Signer has no private-key export. Its cache and issuance work share a single
// nonblocking lock: saturation refuses rather than allocating issuance waiters.
type Signer struct {
	mu      sync.Mutex
	clock   Clock
	entropy io.Reader
	root    *x509.Certificate
	key     *ecdsa.PrivateKey
	cache   map[string]*tls.Certificate
}

func serial(entropy io.Reader) (*big.Int, error) {
	b := make([]byte, 20)
	if _, err := io.ReadFull(entropy, b); err != nil {
		return nil, ErrUnavailable
	}
	b[0] &= 0x7f
	b[0] |= 1
	return new(big.Int).SetBytes(b), nil
}

func generate(installation string, clock Clock, entropy io.Reader) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), entropy)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	sn, err := serial(entropy)
	if err != nil {
		return nil, nil, err
	}
	now := clock.Now().UTC()
	template := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: "Agent Gateway interception CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(contract.HTTPCALifetime), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	cert, err := x509.CreateCertificate(entropy, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	defer clear(der)
	payload, err := json.Marshal(envelope{Version: 1, Installation: installation, Key: der, Certificate: cert})
	if err != nil || len(payload) > contract.HTTPCAEnvelopeBytes {
		clear(payload)
		return nil, nil, ErrUnavailable
	}
	return payload, cert, nil
}

func decode(payload, public []byte, installation string, clock Clock, entropy io.Reader) (*Signer, error) {
	var e envelope
	if strictjson.Decode(payload, &e, strictjson.Options{MaxBytes: contract.HTTPCAEnvelopeBytes, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
		return nil, ErrUnavailable
	}
	defer clear(e.Key)
	if e.Version != 1 || e.Installation != installation || string(e.Certificate) != string(public) {
		return nil, ErrUnavailable
	}
	root, err := publicCertificate(public)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(e.Key)
	if err != nil || key.Curve != elliptic.P256() || !key.PublicKey.Equal(root.PublicKey) {
		return nil, ErrUnavailable
	}
	now := clock.Now()
	if now.Before(root.NotBefore) || !now.Before(root.NotAfter) {
		return nil, ErrUnavailable
	}
	return &Signer{clock: clock, entropy: entropy, root: root, key: key, cache: make(map[string]*tls.Certificate)}, nil
}

func publicCertificate(der []byte) (*x509.Certificate, error) {
	if len(der) == 0 || len(der) > contract.HTTPCAEnvelopeBytes {
		return nil, ErrUnavailable
	}
	c, err := x509.ParseCertificate(der)
	if err != nil || !c.IsCA || !c.BasicConstraintsValid || !c.MaxPathLenZero || c.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign || c.CheckSignatureFrom(c) != nil {
		return nil, ErrUnavailable
	}
	return c, nil
}

func publicPEM(der []byte) ([]byte, error) {
	if _, err := publicCertificate(der); err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func (s *Signer) Certificate(host string) (*tls.Certificate, error) {
	target, err := httppolicy.NewDestination(host, 443)
	if err != nil || target.Host() != host || s == nil || !s.mu.TryLock() {
		return nil, ErrUnavailable
	}
	defer s.mu.Unlock()
	now := s.clock.Now()
	if s.key == nil || now.Before(s.root.NotBefore) || !now.Before(s.root.NotAfter) {
		return nil, ErrUnavailable
	}
	if cert := s.cache[host]; cert != nil && now.Before(cert.Leaf.NotAfter) {
		return cloneCertificate(cert), nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), s.entropy)
	if err != nil {
		return nil, ErrUnavailable
	}
	sn, err := serial(s.entropy)
	if err != nil {
		return nil, err
	}
	until := now.Add(contract.HTTPLeafLifetime)
	if until.After(s.root.NotAfter) {
		until = s.root.NotAfter
	}
	t := &x509.Certificate{SerialNumber: sn, NotBefore: now.Add(-time.Minute), NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	if ip, e := netip.ParseAddr(host); e == nil {
		t.IPAddresses = append(t.IPAddresses, ip.AsSlice())
	} else {
		t.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(s.entropy, t, s.root, &key.PublicKey, s.key)
	if err != nil {
		return nil, ErrUnavailable
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, ErrUnavailable
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	// Bounded whole-cache eviction avoids a second mutable LRU bookkeeping owner.
	if len(s.cache) >= contract.HTTPCAEntries {
		clear(s.cache)
	}
	s.cache[host] = cert
	return cloneCertificate(cert), nil
}

func cloneCertificate(c *tls.Certificate) *tls.Certificate {
	result := *c
	result.Certificate = [][]byte{append([]byte(nil), c.Certificate[0]...)}
	return &result
}

// Close withdraws signing capability and drops process-local cache references.
func (s *Signer) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key = nil
	clear(s.cache)
}
