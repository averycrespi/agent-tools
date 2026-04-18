package inject_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/inject"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubSecrets is a simple in-memory SecretsGetter for tests.
type stubSecrets map[string]stubEntry

type stubEntry struct {
	value string
	scope string
}

func (s stubSecrets) Get(_ context.Context, name, _ string) (string, string, error) {
	e, ok := s[name]
	if !ok {
		return "", "", errors.New("not found")
	}
	return e.value, e.scope, nil
}

// emptySecrets is a store that returns not-found for every key.
type emptySecrets struct{}

func (emptySecrets) Get(_ context.Context, _, _ string) (string, string, error) {
	return "", "", errors.New("not found")
}

func TestTemplate_SecretsExpansion(t *testing.T) {
	store := stubSecrets{
		"gh_bot": {value: "abc", scope: "agent:x"},
	}
	ctx := context.Background()
	got, scope, err := inject.Expand(ctx, "Bearer ${secrets.gh_bot}", "x", store)
	require.NoError(t, err)
	assert.Equal(t, "Bearer abc", got)
	assert.Equal(t, "agent:x", scope)
}

func TestTemplate_AgentName(t *testing.T) {
	ctx := context.Background()
	got, _, err := inject.Expand(ctx, "agent=${agent.name}", "x", nil)
	require.NoError(t, err)
	assert.Equal(t, "agent=x", got)
}

func TestTemplate_UnresolvedSecret_ReturnsError(t *testing.T) {
	ctx := context.Background()
	_, _, err := inject.Expand(ctx, "Bearer ${secrets.missing}", "x", emptySecrets{})
	assert.ErrorIs(t, err, inject.ErrSecretUnresolved)
}

func TestTemplate_OpaqueValues(t *testing.T) {
	store := stubSecrets{
		"x": {value: `has ${nested} and \ chars`, scope: "global"},
	}
	ctx := context.Background()
	got, _, err := inject.Expand(ctx, "${secrets.x}", "agent", store)
	require.NoError(t, err)
	// The secret value must NOT be re-expanded.
	assert.Equal(t, `has ${nested} and \ chars`, got)
}

func TestTemplate_UnknownExpression_ReturnsError(t *testing.T) {
	ctx := context.Background()
	_, _, err := inject.Expand(ctx, "${env.FOO}", "x", nil)
	assert.Error(t, err)
	assert.ErrorIs(t, err, inject.ErrUnknownExpression)
}

func TestInjector_ReplaceHeaderOverwrites(t *testing.T) {
	store := stubSecrets{
		"tok": {value: "secret", scope: "global"},
	}
	inj := inject.NewInjector(store, time.Minute)

	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "old")

	rule := &rules.Rule{
		Inject: &rules.Inject{
			ReplaceHeaders: map[string]string{
				"Authorization": "Bearer ${secrets.tok}",
			},
		},
	}

	status, scope, err := inj.Apply(context.Background(), req, rule, "agent1")
	require.NoError(t, err)
	assert.Equal(t, inject.StatusApplied, status)
	assert.Equal(t, "global", scope)
	assert.Equal(t, "Bearer secret", req.Header.Get("Authorization"))
}

func TestInjector_RemoveHeader(t *testing.T) {
	inj := inject.NewInjector(nil, time.Minute)

	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.NoError(t, err)
	req.Header.Set("X-Internal", "leaky")

	rule := &rules.Rule{
		Inject: &rules.Inject{
			RemoveHeaders: []string{"X-Internal"},
		},
	}

	status, _, err := inj.Apply(context.Background(), req, rule, "agent1")
	require.NoError(t, err)
	assert.Equal(t, inject.StatusApplied, status)
	assert.Equal(t, "", req.Header.Get("X-Internal"))
}

func TestInjector_NoInjectBlock_StatusNoInject(t *testing.T) {
	inj := inject.NewInjector(nil, time.Minute)

	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.NoError(t, err)

	rule := &rules.Rule{} // Inject is nil

	status, _, err := inj.Apply(context.Background(), req, rule, "agent1")
	require.NoError(t, err)
	assert.Equal(t, inject.StatusNoInject, status)
}

func TestInjector_UnresolvedSecret_StatusFailed(t *testing.T) {
	inj := inject.NewInjector(emptySecrets{}, time.Minute)

	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "original")

	rule := &rules.Rule{
		Inject: &rules.Inject{
			ReplaceHeaders: map[string]string{
				"Authorization": "Bearer ${secrets.missing}",
			},
		},
	}

	status, _, err := inj.Apply(context.Background(), req, rule, "agent1")
	assert.Equal(t, inject.StatusFailed, status)
	assert.ErrorIs(t, err, inject.ErrSecretUnresolved)
	// Request must be unchanged.
	assert.Equal(t, "original", req.Header.Get("Authorization"))
}

func TestCache_TTLExpiry(t *testing.T) {
	c := inject.NewCache(50 * time.Millisecond)

	c.Set("agent1", "key", "val1", "", time.Now().Add(50*time.Millisecond))
	v, _, ok := c.Get("agent1", "key")
	assert.True(t, ok)
	assert.Equal(t, "val1", v)

	time.Sleep(60 * time.Millisecond)
	_, _, ok = c.Get("agent1", "key")
	assert.False(t, ok, "entry should have expired")
}

func TestCache_InvalidateClearsAll(t *testing.T) {
	c := inject.NewCache(time.Minute)

	c.Set("a", "k1", "v1", "", time.Now().Add(time.Minute))
	c.Set("b", "k2", "v2", "", time.Now().Add(time.Minute))

	c.Invalidate()

	_, _, ok1 := c.Get("a", "k1")
	_, _, ok2 := c.Get("b", "k2")
	assert.False(t, ok1)
	assert.False(t, ok2)
}

func TestCache_StoresAndReturnsScope(t *testing.T) {
	c := inject.NewCache(time.Minute)

	c.Set("agent1", "tok", "secret-value", "agent:x", time.Time{})
	value, scope, ok := c.Get("agent1", "tok")
	require.True(t, ok)
	assert.Equal(t, "secret-value", value)
	assert.Equal(t, "agent:x", scope)
}

func TestInjector_ScopeConsistentAcrossCachedApplies(t *testing.T) {
	store := stubSecrets{
		"tok": {value: "secret", scope: "agent:x"},
	}
	inj := inject.NewInjector(store, time.Minute)

	rule := &rules.Rule{
		Inject: &rules.Inject{
			ReplaceHeaders: map[string]string{
				"Authorization": "Bearer ${secrets.tok}",
			},
		},
	}

	ctx := context.Background()

	req1, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.NoError(t, err)
	status1, scope1, err := inj.Apply(ctx, req1, rule, "agent1")
	require.NoError(t, err)
	assert.Equal(t, inject.StatusApplied, status1)

	// Second Apply hits the cache — scope must still be returned correctly.
	req2, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.NoError(t, err)
	status2, scope2, err := inj.Apply(ctx, req2, rule, "agent1")
	require.NoError(t, err)
	assert.Equal(t, inject.StatusApplied, status2)

	assert.Equal(t, scope1, scope2, "scope must be identical on cache hit")
	assert.Equal(t, "agent:x", scope1)
}
