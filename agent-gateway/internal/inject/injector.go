package inject

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
)

// InjectionStatus describes the outcome of an Apply call.
type InjectionStatus int

const (
	// StatusNoInject means the rule has no inject block; the request is
	// unchanged.
	StatusNoInject InjectionStatus = iota
	// StatusApplied means all header mutations were applied successfully.
	StatusApplied
	// StatusFailed means at least one template expansion failed (e.g. unresolved
	// secret). The request is left unchanged.
	StatusFailed
)

// Injector applies header mutations defined in a rule's inject block to an
// outgoing HTTP request. It caches resolved secret values for the configured
// TTL.
type Injector struct {
	store SecretsGetter
	cache *Cache
}

// NewInjector creates an Injector backed by store with a cache using the given
// TTL. store may be nil if no rules use secret templates.
func NewInjector(store SecretsGetter, ttl time.Duration) *Injector {
	return &Injector{
		store: store,
		cache: NewCache(ttl),
	}
}

// scopedCachingStore wraps SecretsGetter with an in-process TTL cache.
// Both value and scope are stored together so that the scope is correctly
// returned on cache hits as well as cache misses.
type scopedCachingStore struct {
	inner SecretsGetter
	cache *Cache
}

func (s *scopedCachingStore) Get(ctx context.Context, name, agent string) (string, string, error) {
	if value, scope, ok := s.cache.Get(agent, name); ok {
		return value, scope, nil
	}
	value, scope, err := s.inner.Get(ctx, name, agent)
	if err != nil {
		return "", "", err
	}
	s.cache.Set(agent, name, value, scope, time.Time{})
	return value, scope, nil
}

// Apply applies the inject block of rule to req for the given agent.
//
// Algorithm:
//  1. If rule.Inject is nil, return StatusNoInject immediately.
//  2. Expand ALL set_header templates first, collecting (name→value). On the
//     first unresolved secret, return StatusFailed without touching req.
//  3. Apply set_header mutations to req.
//  4. Apply remove_header deletions.
//  5. Return StatusApplied and the first credential scope encountered.
func (inj *Injector) Apply(ctx context.Context, req *http.Request, rule *rules.Rule, agent string) (InjectionStatus, string, error) {
	if rule.Inject == nil {
		return StatusNoInject, "", nil
	}

	// Wrap store with a scoped cache so we don't hit the backing store twice
	// for the same secret within one Apply call.
	var effectiveStore SecretsGetter
	if inj.store != nil {
		effectiveStore = &scopedCachingStore{
			inner: inj.store,
			cache: inj.cache,
		}
	}

	// Phase 1: expand all set_header values. Collect results before mutating.
	type headerMutation struct {
		name  string
		value string
	}
	mutations := make([]headerMutation, 0, len(rule.Inject.SetHeaders))
	var firstScope string

	// Iterate in sorted order for deterministic behaviour.
	headerNames := make([]string, 0, len(rule.Inject.SetHeaders))
	for k := range rule.Inject.SetHeaders {
		headerNames = append(headerNames, k)
	}
	sort.Strings(headerNames)

	for _, hName := range headerNames {
		tmpl := rule.Inject.SetHeaders[hName]
		expanded, scope, err := Expand(ctx, tmpl, agent, effectiveStore)
		if err != nil {
			return StatusFailed, "", fmt.Errorf("inject set_header %q: %w", hName, err)
		}
		if firstScope == "" && scope != "" {
			firstScope = scope
		}
		mutations = append(mutations, headerMutation{name: hName, value: expanded})
	}

	// Phase 2: mutate the request only after all expansions succeed.
	for _, m := range mutations {
		req.Header.Set(m.name, m.value)
	}
	for _, h := range rule.Inject.RemoveHeaders {
		req.Header.Del(h)
	}

	return StatusApplied, firstScope, nil
}

// InvalidateCache clears all cached secret values.
func (inj *Injector) InvalidateCache() {
	inj.cache.Invalidate()
}
