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
	"sync"
	"time"
)

const (
	defaultLeafLifetime  = 24 * time.Hour
	defaultSweepInterval = 5 * time.Minute
	defaultSweepBuffer   = 1 * time.Hour
)

// cacheEntry holds an issued leaf certificate alongside the ready-to-use
// *tls.Config so both can be swept together.
type cacheEntry struct {
	leaf *x509.Certificate
	cfg  *tls.Config
}

// leafCache maps hostname → *cacheEntry using sync.Map for lock-free reads.
type leafCache struct {
	m sync.Map
}

func (c *leafCache) load(host string) (*cacheEntry, bool) {
	v, ok := c.m.Load(host)
	if !ok {
		return nil, false
	}
	return v.(*cacheEntry), true
}

// store stores the entry only if no entry is already present (LoadOrStore).
// It returns the canonical entry (existing or newly stored).
func (c *leafCache) store(host string, e *cacheEntry) *cacheEntry {
	actual, _ := c.m.LoadOrStore(host, e)
	return actual.(*cacheEntry)
}

func (c *leafCache) delete(host string) {
	c.m.Delete(host)
}

func (c *leafCache) rangeAll(fn func(host string, e *cacheEntry) bool) {
	c.m.Range(func(k, v any) bool {
		return fn(k.(string), v.(*cacheEntry))
	})
}

// ---------------------------------------------------------------------------
// Authority extensions
// ---------------------------------------------------------------------------

// initLeafFields sets the leaf-issuance defaults on a freshly created
// Authority.  Called at the end of both load() and generate().
func initLeafFields(a *Authority) {
	a.cache = &leafCache{}
	a.leafLifetime = defaultLeafLifetime
	a.sweepBuffer = defaultSweepBuffer
	a.sweepInterval = defaultSweepInterval
}

// ServerConfig returns a *tls.Config whose Certificates slice contains a
// freshly issued (or cached) leaf certificate for host signed by the root CA.
// The same pointer is returned on subsequent calls for the same host.
func (a *Authority) ServerConfig(host string) (*tls.Config, error) {
	if e, ok := a.cache.load(host); ok {
		return e.cfg, nil
	}

	e, err := a.issueLeaf(host)
	if err != nil {
		return nil, err
	}

	// Use LoadOrStore so a racing goroutine that just issued the same host
	// doesn't end up with two different configs in flight.
	canonical := a.cache.store(host, e)
	return canonical.cfg, nil
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

	cfg := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"h2", "http/1.1"},
		MinVersion:   tls.VersionTLS12,
	}

	return &cacheEntry{leaf: leaf, cfg: cfg}, nil
}
