package ca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net"
	"net/netip"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/averycrespi/agent-tools/egress-broker/internal/hostnorm"
)

// Leaf certificates are short-lived and cached.
//
// The cache is bounded. An unbounded map is a denial-of-service vector: an
// authenticated agent can CONNECT to unique hosts and grow it without limit.
//
// skewAllowance is applied on every cache hit as well as at issuance. If a
// cached certificate expires within the allowance, or its NotBefore is more
// than the allowance in the future, it is dropped and reissued — otherwise a
// client whose clock is a few minutes off sees an opaque TLS handshake
// failure instead of a working connection.
const (
	defaultLeafLifetime  = 24 * time.Hour
	defaultSweepInterval = 5 * time.Minute
	defaultSweepBuffer   = 1 * time.Hour
	skewAllowance        = 5 * time.Minute

	lruCap = 10_000
)

type cacheEntry struct {
	leaf *x509.Certificate
	cfg  *tls.Config
}

type leafCache struct {
	m *lru.Cache[string, *cacheEntry]
}

func newLeafCache() *leafCache {
	// lru.New only errors on a non-positive size, and lruCap is a positive
	// compile-time constant.
	c, err := lru.New[string, *cacheEntry](lruCap)
	if err != nil {
		panic(fmt.Sprintf("ca: leaf cache size %d rejected: %v", lruCap, err))
	}
	return &leafCache{m: c}
}

func (c *leafCache) load(host string) (*cacheEntry, bool) { return c.m.Get(host) }

// store adds the entry only if none is present, returning whichever entry is
// canonical. Two goroutines issuing for the same host must not end up serving
// two different configs.
func (c *leafCache) store(host string, e *cacheEntry) *cacheEntry {
	existing, loaded, _ := c.m.PeekOrAdd(host, e)
	if loaded {
		return existing
	}
	return e
}

func (c *leafCache) delete(host string) { c.m.Remove(host) }
func (c *leafCache) purge()             { c.m.Purge() }
func (c *leafCache) len() int           { return c.m.Len() }

func (c *leafCache) rangeAll(fn func(host string, e *cacheEntry) bool) {
	for _, key := range c.m.Keys() {
		v, ok := c.m.Peek(key)
		if !ok {
			continue
		}
		if !fn(key, v) {
			return
		}
	}
}

func initLeafFields(a *Authority) {
	a.cache = newLeafCache()
	a.leafLifetime = defaultLeafLifetime
	a.sweepBuffer = defaultSweepBuffer
	a.sweepInterval = defaultSweepInterval
	a.skewBuffer = skewAllowance
}

// ServerConfig returns a *tls.Config carrying a leaf certificate for host,
// signed by the root, issuing one if the cache has no usable entry.
//
// host is canonicalised before use as the cache key, so "API.github.com" and
// "api.github.com" share one certificate. An input that fails normalisation is
// used as-is: the interception decision has already been made by this point,
// and failing the handshake here would be a worse outcome than a certificate
// with an odd subject.
func (a *Authority) ServerConfig(host string) (*tls.Config, error) {
	if canon, err := hostnorm.Normalize(host); err == nil {
		host = canon
	}

	if e, ok := a.cache.load(host); ok && a.certUsable(e) {
		return e.cfg, nil
	}
	a.cache.delete(host)

	e, err := a.issueLeaf(host)
	if err != nil {
		return nil, err
	}
	return a.cache.store(host, e).cfg, nil
}

// certUsable reports whether a cached certificate is outside the skew window
// at both ends.
//
// NotBefore more than skewBuffer in the future: a client whose clock is behind
// ours rejects it as not yet valid. NotAfter within skewBuffer: a client whose
// clock is ahead already sees it as expired.
func (a *Authority) certUsable(e *cacheEntry) bool {
	now := time.Now()
	if now.Add(a.skewBuffer).Before(e.leaf.NotBefore) {
		return false
	}
	if now.After(e.leaf.NotAfter.Add(-a.skewBuffer)) {
		return false
	}
	return true
}

// Start runs a background sweep that drops soon-to-expire cache entries. It
// returns immediately; the goroutine exits when ctx is cancelled.
func (a *Authority) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(a.sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.sweepExpired()
			}
		}
	}()
}

func (a *Authority) sweepExpired() {
	cutoff := time.Now().Add(a.sweepBuffer)
	var stale []string
	a.cache.rangeAll(func(host string, e *cacheEntry) bool {
		if e.leaf.NotAfter.Before(cutoff) {
			stale = append(stale, host)
		}
		return true
	})
	for _, host := range stale {
		a.cache.delete(host)
	}
}

func (a *Authority) issueLeaf(host string) (*cacheEntry, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: subjectCommonName(host)},
		NotBefore:             now.Add(-skewAllowance),
		NotAfter:              now.Add(a.leafLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// D11: an IP-literal target must be bound as an IP SAN, not a DNS name.
	// A certificate carrying "127.0.0.1" as a DNSName fails verification with
	// no useful message, so MITM of an allowed IP literal would break
	// opaquely.
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		template.IPAddresses = []net.IP{net.IP(addr.Unmap().AsSlice())}
	} else {
		template.DNSNames = []string{host}
	}

	bundle := a.current.Load()
	derBytes, err := x509.CreateCertificate(rand.Reader, template, bundle.cert, &leafKey.PublicKey, bundle.key)
	if err != nil {
		return nil, fmt.Errorf("ca: sign leaf for %q: %w", host, err)
	}
	leaf, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse leaf: %w", err)
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{derBytes},
			PrivateKey:  leafKey,
			Leaf:        leaf, // pre-parsed so handshakes do not re-parse
		}},
		NextProtos: []string{"h2", "http/1.1"},
		// The MITM termination point only ever talks to clients inside our own
		// sandbox, so requiring TLS 1.3 costs nothing and removes downgrade
		// negotiation and legacy cipher suites entirely.
		MinVersion: tls.VersionTLS13,
	}

	return &cacheEntry{leaf: leaf, cfg: cfg}, nil
}

// subjectCommonName truncates to the 64-byte limit x509 imposes on CN. A
// longer host is still bound correctly by its SAN, which is what verification
// actually consults.
func subjectCommonName(host string) string {
	const maxCN = 64
	if len(host) <= maxCN {
		return host
	}
	return host[:maxCN]
}
