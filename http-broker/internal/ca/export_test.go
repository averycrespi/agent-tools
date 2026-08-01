package ca

import "time"

// Test hooks. The knobs below are unexported so callers cannot tune the
// security-relevant lifetimes at runtime, but tests need to drive the skew and
// sweep behaviour without waiting real hours.

// SetLeafLifetime overrides how long issued leaves are valid.
func (a *Authority) SetLeafLifetime(d time.Duration) { a.leafLifetime = d }

// SetSkewBuffer overrides the clock-skew allowance applied on cache hits.
func (a *Authority) SetSkewBuffer(d time.Duration) { a.skewBuffer = d }

// SetSweepBuffer overrides how far ahead the sweeper looks for expiry.
func (a *Authority) SetSweepBuffer(d time.Duration) { a.sweepBuffer = d }

// CacheLen reports how many leaves are currently cached.
func (a *Authority) CacheLen() int { return a.cache.len() }

// SweepExpired runs one sweep synchronously.
func (a *Authority) SweepExpired() { a.sweepExpired() }

// LRUCap exposes the cache bound so a test can prove eviction at it.
const LRUCap = lruCap
