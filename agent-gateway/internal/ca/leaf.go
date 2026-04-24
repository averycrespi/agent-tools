package ca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostnorm"
)

// Leaf certs are issued with a 24h lifetime and evicted from the cache 1h
// before NotAfter by the background sweeper. An additional clock-skew buffer
// (default 5 min) is applied on every cache hit: if the cached cert's NotAfter
// is within skewBuffer, or its NotBefore is more than skewBuffer in the future,
// the entry is dropped and a fresh cert is issued. This prevents handing a
// near-expired cert to a client whose clock is a few minutes fast, which would
// otherwise produce an opaque TLS handshake failure at the sandbox.
//
// The cache is bounded at lruCap entries. An unbounded sync.Map is a DoS vector:
// an authenticated agent can CONNECT to unique hosts to grow the cache without
// limit. Capping at 10 000 entries bounds worst-case memory to roughly
// (10 000 × cert+key size) while still covering any realistic set of upstream
// hosts seen in practice.
const (
	defaultLeafLifetime  = 24 * time.Hour
	defaultSweepInterval = 5 * time.Minute
	defaultSweepBuffer   = 1 * time.Hour
	defaultSkewBuffer    = 5 * time.Minute

	lruCap = 10_000
)

// cacheEntry holds an issued leaf certificate alongside the ready-to-use
// *tls.Config so both can be swept together.
type cacheEntry struct {
	leaf *x509.Certificate
	cfg  *tls.Config
}

// leafCache wraps an LRU cache mapping hostname → *cacheEntry. The LRU cap
// bounds worst-case memory; eviction is automatic on Add when len > lruCap.
type leafCache struct {
	m *lru.Cache[string, *cacheEntry]
}

func newLeafCache() *leafCache {
	c, err := lru.New[string, *cacheEntry](lruCap)
	if err != nil {
		// lru.New only errors on non-positive size; lruCap is a compile-time
		// positive constant, so this is unreachable.
		panic(err)
	}
	return &leafCache{m: c}
}

func (c *leafCache) load(host string) (*cacheEntry, bool) {
	return c.m.Get(host)
}

// store stores the entry only if no entry is already present (add-if-absent).
// It returns the canonical entry (existing or newly stored).
func (c *leafCache) store(host string, e *cacheEntry) *cacheEntry {
	existing, loaded, _ := c.m.PeekOrAdd(host, e)
	if loaded {
		return existing
	}
	return e
}

func (c *leafCache) delete(host string) {
	c.m.Remove(host)
}

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

// ---------------------------------------------------------------------------
// Authority extensions
// ---------------------------------------------------------------------------

// initLeafFields sets the leaf-issuance defaults on a freshly created
// Authority.  Called at the end of both load() and generate().
func initLeafFields(a *Authority) {
	a.cache = newLeafCache()
	a.leafLifetime = defaultLeafLifetime
	a.sweepBuffer = defaultSweepBuffer
	a.sweepInterval = defaultSweepInterval
	a.skewBuffer = defaultSkewBuffer
}

// ServerConfig returns a *tls.Config whose Certificates slice contains a
// freshly issued (or cached) leaf certificate for host signed by the root CA.
// The same pointer is returned on subsequent calls for the same host.
//
// host is canonicalised via hostnorm before use as the cache key so that
// "API.github.com" and "api.github.com" share a single cert. Inputs that
// fail normalization are used as-is (the proxy layer has already made the
// intercept decision; we should not fail handshake here).
//
// Cache hits are validated against a clock-skew window: if the cert expires
// within skewBuffer, or its NotBefore is more than skewBuffer in the future,
// the stale entry is evicted and a fresh cert is issued.
func (a *Authority) ServerConfig(host string) (*tls.Config, error) {
	if canon, err := hostnorm.Normalize(host); err == nil {
		host = canon
	}
	if e, ok := a.cache.load(host); ok && a.certValid(e) {
		return e.cfg, nil
	}

	// Either no cached entry or the cached cert is within the skew window;
	// evict any stale entry and issue a fresh one.
	a.cache.delete(host)

	e, err := a.issueLeaf(host)
	if err != nil {
		return nil, err
	}

	// Use store (PeekOrAdd) so a racing goroutine that just issued the same
	// host doesn't end up with two different configs in flight.
	canonical := a.cache.store(host, e)
	return canonical.cfg, nil
}

// certValid reports whether the cached entry is outside the clock-skew window
// on both ends:
//   - NotBefore: if the cert's NotBefore is more than skewBuffer in the future
//     relative to now, clients whose clocks are up to skewBuffer behind ours
//     would reject it as "not yet valid"; re-issue so NotBefore is closer to now.
//   - NotAfter: if the cert expires within skewBuffer of now, clients whose
//     clocks are up to skewBuffer ahead of ours would already see it as expired;
//     re-issue to ensure remaining validity exceeds the skew window.
func (a *Authority) certValid(e *cacheEntry) bool {
	now := time.Now()
	// NotBefore is more than skewBuffer in the future: slow-clock clients reject it.
	if now.Add(a.skewBuffer).Before(e.leaf.NotBefore) {
		return false
	}
	// NotAfter is within skewBuffer: fast-clock clients may already see it expired.
	if now.After(e.leaf.NotAfter.Add(-a.skewBuffer)) {
		return false
	}
	return true
}

// Start spawns a background goroutine that periodically sweeps expired entries
// from the leaf cache.  It returns promptly; the goroutine exits when ctx is
// cancelled.
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

// sweepExpired removes cache entries whose leaf certificate will expire within
// the sweep buffer window.
func (a *Authority) sweepExpired() {
	cutoff := time.Now().Add(a.sweepBuffer)
	var toDelete []string
	a.cache.rangeAll(func(host string, e *cacheEntry) bool {
		if e.leaf.NotAfter.Before(cutoff) {
			toDelete = append(toDelete, host)
		}
		return true
	})
	for _, host := range toDelete {
		a.cache.delete(host)
	}
}

// ---------------------------------------------------------------------------
// Leaf issuance
// ---------------------------------------------------------------------------

func (a *Authority) issueLeaf(host string) (*cacheEntry, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
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
			CommonName: host,
		},
		NotBefore:             now,
		NotAfter:              now.Add(a.leafLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Place IP literals in IPAddresses; DNS names in DNSNames.
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	bundle := a.current.Load()
	derBytes, err := x509.CreateCertificate(rand.Reader, template, bundle.cert, &leafKey.PublicKey, bundle.key)
	if err != nil {
		return nil, err
	}

	leaf, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, err
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  leafKey,
		// Pre-parse so TLS handshakes don't need to re-parse.
		Leaf: leaf,
	}

	// WHY: VersionTLS13 drops TLS 1.0/1.1/1.2 cipher rollback attack paths.
	// The MITM termination point only talks to our own sandbox clients (the
	// agent's HTTP library), which we control; requiring TLS 1.3 eliminates
	// downgrade negotiation entirely and removes legacy cipher suites.
	cfg := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"h2", "http/1.1"},
		MinVersion:   tls.VersionTLS13,
	}

	return &cacheEntry{leaf: leaf, cfg: cfg}, nil
}
