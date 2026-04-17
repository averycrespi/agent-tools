// export_test.go exposes internal helpers for white-box testing.
// This file is compiled only during `go test`.
package ca

import (
	"crypto/tls"
	"time"
)

// SetLeafLifetimeForTest overrides the leaf certificate validity period on a so
// that tests can issue very short-lived certificates to exercise the sweeper.
func SetLeafLifetimeForTest(a *Authority, d time.Duration) {
	a.leafLifetime = d
}

// CacheLookupForTest returns the cached *tls.Config for host (and whether it
// was present), without issuing a new cert.
func CacheLookupForTest(a *Authority, host string) (*tls.Config, bool) {
	e, ok := a.cache.load(host)
	if !ok {
		return nil, false
	}
	return e.cfg, true
}

// SweepOnceForTest runs a single sweep pass synchronously.
func SweepOnceForTest(a *Authority) {
	a.sweepExpired()
}
