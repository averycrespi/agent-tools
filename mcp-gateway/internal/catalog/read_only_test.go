package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadOnlyHintDefaultsAndMalformedAnnotations(t *testing.T) {
	for _, raw := range []string{`{"name":"echo","inputSchema":{"type":"object"}}`, `{"name":"echo","inputSchema":{"type":"object"},"annotations":null}`, `{"name":"echo","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":null}}`, `{"name":"echo","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":false}}`} {
		normalized := normalizeCallTool(t, "00000000000000000000000051", "sample.echo", raw)
		require.False(t, normalized.Descriptor.Annotations.ReadOnlyHint)
	}
	for _, value := range []string{`"true"`, `1`, `{}`, `[]`} {
		_, err := normalizeAnnotations([]byte(`{"readOnlyHint":` + value + `}`))
		require.ErrorIs(t, err, ErrDescriptorInvalid)
	}
}

func TestReadOnlyDescriptorReplacementFencesPinnedAuthority(t *testing.T) {
	repository, servers, _, _ := newCatalogRepository(t)
	server := createCatalogServer(t, servers, "sample")
	registry, err := NewActiveRegistry(repository, &catalogClock{now: catalogTime}, activeProcessID)
	require.NoError(t, err)
	runtime, _ := newRouteRuntime(t)
	firstTool := normalizeCallTool(t, server.ID, "sample.echo", `{"name":"echo","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}`)
	firstStatus, err := registry.Publish(t.Context(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime", RuntimeGeneration: 1, Candidate: NormalizedCandidate{Tools: []NormalizedTool{firstTool}, RawCount: 1, Pages: 1}, Current: func() bool { return true }, Runtime: runtime})
	require.NoError(t, err)
	first, found := registry.Routes().ResolveCall("sample.echo")
	require.True(t, found)
	require.True(t, first.ReadOnlyHint)
	lease, err := first.Capability.Acquire(t.Context())
	require.NoError(t, err)
	secondTool := normalizeCallTool(t, server.ID, "sample.echo", `{"name":"echo","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":false}}`)
	future := normalizeCallTool(t, server.ID, "sample.future", `{"name":"future","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}`)
	_, err = registry.Publish(t.Context(), Publication{Fence: catalogFence(server.ID, *firstStatus.Revision), RuntimeID: "runtime", RuntimeGeneration: 1, Candidate: NormalizedCandidate{Tools: []NormalizedTool{secondTool, future}, RawCount: 2, Pages: 1}, Current: func() bool { return true }, Runtime: runtime})
	require.NoError(t, err)
	_, err = first.Capability.Acquire(t.Context())
	require.Error(t, err)
	result := lease.Execute(t.Context(), []byte(`{}`))
	require.Error(t, result.Err)
	second, found := registry.Routes().ResolveCall("sample.echo")
	require.True(t, found)
	require.False(t, second.ReadOnlyHint)
	require.NotSame(t, first.Capability, second.Capability)
	added, found := registry.Routes().ResolveCall("sample.future")
	require.True(t, found)
	require.True(t, added.ReadOnlyHint)
}
