// export_test.go exposes internal helpers for white-box testing.
// This file is compiled only during `go test`.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"time"
)

// SetLeafLifetimeForTest overrides the leaf certificate validity period on a so
// that tests can issue very short-lived certificates to exercise the sweeper.
func SetLeafLifetimeForTest(a *Authority, d time.Duration) {
	a.leafLifetime = d
}

// SetSkewBufferForTest overrides the clock-skew buffer on a so that tests can
// exercise the validity window checks applied to cache hits.
func SetSkewBufferForTest(a *Authority, d time.Duration) {
	a.skewBuffer = d
}

// CacheLookupForTest returns the cached *tls.Config for host (and whether it
// was present), without issuing a new cert and without affecting LRU order.
func CacheLookupForTest(a *Authority, host string) (*tls.Config, bool) {
	e, ok := a.cache.m.Peek(host)
	if !ok {
		return nil, false
	}
	return e.cfg, true
}

// SweepOnceForTest runs a single sweep pass synchronously.
func SweepOnceForTest(a *Authority) {
	a.sweepExpired()
}

// InjectCacheEntryForTest replaces the cache entry for host with a cert whose
// NotBefore is set to notBefore (NotAfter = notBefore + 24h). Used to test the
// skew-buffer check on cache hits without waiting for real time to pass.
func InjectCacheEntryForTest(a *Authority, host string, notBefore time.Time) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	serial, err := randomSerial()
	if err != nil {
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
		template.DNSNames = nil
	}
	bundle := a.current.Load()
	derBytes, err := x509.CreateCertificate(rand.Reader, template, bundle.cert, &leafKey.PublicKey, bundle.key)
	if err != nil {
		panic(err)
	}
	leaf, err := x509.ParseCertificate(derBytes)
	if err != nil {
		panic(err)
	}
	tlsCert := tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  leafKey,
		Leaf:        leaf,
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"h2", "http/1.1"},
		MinVersion:   tls.VersionTLS13,
	}
	a.cache.store(host, &cacheEntry{leaf: leaf, cfg: cfg})
}
