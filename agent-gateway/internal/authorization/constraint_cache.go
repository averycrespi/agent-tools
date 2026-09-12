package authorization

import (
	"container/list"
	"strings"
	"sync"
)

const (
	maxCompiledConstraintCacheWeight  = 128 * 1024 * 1024
	maxCompiledConstraintCacheEntries = 4096
	maxCompiledConstraintFlights      = 4
)

type compiledConstraintCache struct {
	sync.Mutex
	weight  int64
	entries map[string]*list.Element
	order   list.List
	flights map[string]*constraintFlight
}

type cachedConstraint struct {
	source   string
	compiled CompiledConstraint
	weight   int64
}

type constraintFlight struct {
	done     chan struct{}
	compiled CompiledConstraint
	err      error
}

// Only transaction-owned readers use this cache. Revisions select rows, not
// compiler semantics; sharing exact immutable bytes cannot share a decision.
func (repository *Repository) compileConstraint(_ string, source string) (CompiledConstraint, error) {
	if int64(len(source)) > mustLimit("constraint_bytes") {
		return CompiledConstraint{}, invalidConstraint("JSON is malformed")
	}
	cache := &repository.constraintCache
	cache.Lock()
	if element := cache.entries[source]; element != nil {
		compiled := element.Value.(cachedConstraint).compiled
		cache.Unlock()
		return compiled, nil
	}
	if flight := cache.flights[source]; flight != nil {
		cache.Unlock()
		<-flight.done
		return flight.compiled, flight.err
	}
	// Storage has four connections, including supplied mutation transactions.
	// This is a backstop, not an additional queue or an uncached compile fallback.
	if len(cache.flights) >= maxCompiledConstraintFlights {
		cache.Unlock()
		return CompiledConstraint{}, ErrResourceLimit
	}
	if cache.flights == nil {
		cache.flights = make(map[string]*constraintFlight)
	}
	source = strings.Clone(source)
	flight := &constraintFlight{done: make(chan struct{})}
	cache.flights[source] = flight
	cache.Unlock()

	compiled, err := repository.compileSource(source)
	weight := int64(len(source)+256) + compiled.cacheWeight()
	cache.Lock()
	if err == nil && weight <= maxCompiledConstraintCacheWeight {
		for len(cache.entries) >= maxCompiledConstraintCacheEntries || cache.weight+weight > maxCompiledConstraintCacheWeight {
			oldest := cache.order.Front()
			entry := oldest.Value.(cachedConstraint)
			delete(cache.entries, entry.source)
			cache.order.Remove(oldest)
			cache.weight -= entry.weight
		}
		if cache.entries == nil {
			cache.entries = make(map[string]*list.Element)
		}
		cache.entries[source] = cache.order.PushBack(cachedConstraint{source: source, compiled: compiled, weight: weight})
		cache.weight += weight
	}
	flight.compiled, flight.err = compiled, err
	delete(cache.flights, source)
	close(flight.done)
	cache.Unlock()
	return compiled, err
}
