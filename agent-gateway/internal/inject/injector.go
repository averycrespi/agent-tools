package inject

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
)

// ErrSecretInvalid is returned when a resolved secret value contains a byte
// that is unsafe to place in an HTTP header (CR, LF, DEL, or any C0 control
// character other than TAB). This is the last defensive layer before a
// credential hits the wire: http.Header.Set has historically allowed some
// CRLF-adjacent bytes through, especially on the HTTP/2 path, so the proxy
// validates bytes itself rather than trusting downstream stacks. A scope
// mismatch surfaces as a 403 in the pipeline — fail-soft would silently drop
// the credential and hand the upstream a partial request.
var ErrSecretInvalid = errors.New("secret value invalid")

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
// Value, scope, and allowed_hosts are stored together so the full tuple
// is returned on cache hits as well as cache misses. Cached allowed_hosts
// means bind/unbind CLI actions only take effect after SIGHUP invalidates
// the cache — the same cadence that applies to value rotations.
type scopedCachingStore struct {
	inner SecretsGetter
	cache *Cache
}

func (s *scopedCachingStore) Get(ctx context.Context, name, agent string) (string, string, []string, error) {
	if value, scope, hosts, ok := s.cache.Get(agent, name); ok {
		return value, scope, hosts, nil
	}
	value, scope, hosts, err := s.inner.Get(ctx, name, agent)
	if err != nil {
		return "", "", nil, err
	}
	s.cache.Set(agent, name, value, scope, hosts, time.Time{})
	return value, scope, hosts, nil
}

// Apply applies the inject block of rule to req for the given agent.
//
// host is the request's target hostname (no port), already normalised via
// hostnorm.Normalize. It is passed through to template expansion so each
// ${secrets.X} reference can assert the host is in the secret's
// allowed_hosts list before being injected. A scope mismatch surfaces as
// ErrSecretHostScopeViolation wrapped by StatusFailed.
//
// Algorithm:
//  1. If rule.Inject is nil, return StatusNoInject immediately.
//  2. Expand ALL replace_header templates first, collecting (name→value). On
//     the first unresolved secret or scope violation, return StatusFailed
//     without touching req.
//  3. Apply replace_header mutations to req.
//  4. Apply remove_header deletions.
//  5. Return StatusApplied and the first credential scope encountered.
func (inj *Injector) Apply(ctx context.Context, req *http.Request, rule *rules.Rule, agent, host string) (InjectionStatus, string, error) {
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

	// Phase 1: expand all replace_header values. Collect results before mutating.
	type headerMutation struct {
		name  string
		value string
	}
	mutations := make([]headerMutation, 0, len(rule.Inject.ReplaceHeaders))
	var firstScope string

	// Iterate in sorted order for deterministic behaviour.
	headerNames := make([]string, 0, len(rule.Inject.ReplaceHeaders))
	for k := range rule.Inject.ReplaceHeaders {
		headerNames = append(headerNames, k)
	}
	sort.Strings(headerNames)

	for _, hName := range headerNames {
		tmpl := rule.Inject.ReplaceHeaders[hName]
		expanded, scope, err := Expand(ctx, tmpl, agent, host, effectiveStore)
		if err != nil {
			return StatusFailed, "", fmt.Errorf("inject replace_header %q: %w", hName, err)
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
