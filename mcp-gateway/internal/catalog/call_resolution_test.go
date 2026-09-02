package catalog

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCallReturnsOnePinnedTargetWithoutAcquiringCapacity(t *testing.T) {
	repository, serverRepository, _, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, &catalogClock{now: catalogTime}, activeProcessID)
	require.NoError(t, err)
	runtime, _ := newRouteRuntime(t)
	normalized := normalizeCallTool(t, server.ID, "sample.echo", `{"name":"echo","inputSchema":{"type":"object","required":["count"],"properties":{"count":{"type":"integer"}},"additionalProperties":false}}`)
	status, err := registry.Publish(context.Background(), Publication{
		Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1,
		Candidate: NormalizedCandidate{Tools: []NormalizedTool{normalized}, RawCount: 1, Pages: 1},
		Current:   func() bool { return true }, Runtime: runtime,
	})
	require.NoError(t, err)
	require.NotNil(t, status.Revision)

	before := registry.Routes().Status()
	target, ok := registry.Routes().ResolveCall("sample.echo")
	require.True(t, ok)
	after := registry.Routes().Status()
	assert.Equal(t, before, after, "resolution must not acquire dispatch capacity")
	assert.Equal(t, "sample.echo", target.ExternalName)
	assert.Equal(t, server.ID, target.ServerID)
	assert.NotEmpty(t, target.ToolID)
	assert.Equal(t, "echo", target.UpstreamName)
	assert.Equal(t, *status.Revision, target.DescriptorRevision)
	assert.Equal(t, normalized.Fingerprint, target.DescriptorFingerprint)
	assert.NotNil(t, target.Validator)
	assert.NotNil(t, target.Capability)

	require.NoError(t, target.Validator.Validate(parseCallArguments(t, `{"count":1}`)))
	require.NoError(t, target.Validator.Validate(parseCallArguments(t, `{"count":1.0}`)), "JSON Schema integer semantics accept a zero-fraction lexical number without rewriting it")
	require.ErrorIs(t, target.Validator.Validate(parseCallArguments(t, `{"count":"1"}`)), ErrArgumentsInvalid)
	require.ErrorIs(t, target.Validator.Validate(parseCallArguments(t, `{}`)), ErrArgumentsInvalid)

	target.ExternalName = "changed"
	target.DescriptorFingerprint = "changed"
	again, ok := registry.Routes().ResolveCall("sample.echo")
	require.True(t, ok)
	assert.Equal(t, "sample.echo", again.ExternalName)
	assert.Equal(t, normalized.Fingerprint, again.DescriptorFingerprint)
	assert.Same(t, target.Validator, again.Validator)
	assert.Same(t, target.Capability, again.Capability)
	_, foundByToolID := registry.Routes().Resolve(again.ToolID)
	assert.True(t, foundByToolID, "existing tool-ID resolution remains available")
	_, missing := registry.Routes().ResolveCall("not-discovered-or-active")
	assert.False(t, missing)
}

func TestResolveCallReplacementWithdrawalStaleAndDrainAreCoherent(t *testing.T) {
	repository, serverRepository, _, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, &catalogClock{now: catalogTime}, activeProcessID)
	require.NoError(t, err)
	firstRuntime, _ := newRouteRuntime(t)
	firstTool := normalizeCallTool(t, server.ID, "sample.echo", `{"name":"echo","inputSchema":{"type":"object","properties":{"version":{"const":1}}}}`)
	firstStatus, err := registry.Publish(context.Background(), Publication{
		Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1,
		Candidate: NormalizedCandidate{Tools: []NormalizedTool{firstTool}, RawCount: 1, Pages: 1},
		Current:   func() bool { return true }, Runtime: firstRuntime,
	})
	require.NoError(t, err)
	first, ok := registry.Routes().ResolveCall("sample.echo")
	require.True(t, ok)
	assert.Equal(t, *firstStatus.Revision, first.DescriptorRevision)

	enteredInstall := make(chan struct{})
	releaseInstall := make(chan struct{})
	registry.beforeInstall = func() {
		close(enteredInstall)
		<-releaseInstall
	}
	secondRuntime, _ := newRouteRuntime(t)
	secondTool := normalizeCallTool(t, server.ID, "sample.echo", `{"name":"echo","inputSchema":{"type":"object","properties":{"version":{"const":2}}}}`)
	published := make(chan error, 1)
	go func() {
		_, publishErr := registry.Publish(context.Background(), Publication{
			Fence: catalogFence(server.ID, *firstStatus.Revision), RuntimeID: "runtime-2", RuntimeGeneration: 2,
			Candidate: NormalizedCandidate{Tools: []NormalizedTool{secondTool}, RawCount: 1, Pages: 1},
			Current:   func() bool { return true }, Runtime: secondRuntime,
		})
		published <- publishErr
	}()
	<-enteredInstall
	resolved := make(chan CallTarget, 1)
	go func() {
		target, found := registry.Routes().ResolveCall("sample.echo")
		if found {
			resolved <- target
		}
	}()
	select {
	case <-resolved:
		t.Fatal("resolution observed an in-progress publication")
	case <-time.After(10 * time.Millisecond):
	}
	close(releaseInstall)
	require.NoError(t, <-published)
	second := <-resolved
	assert.Equal(t, secondTool.Fingerprint, second.DescriptorFingerprint)
	assert.NotEqual(t, first.DescriptorRevision, second.DescriptorRevision)
	assert.NotSame(t, first.Capability, second.Capability)
	require.NoError(t, first.Validator.Validate(parseCallArguments(t, `{"version":1}`)))
	require.ErrorIs(t, first.Validator.Validate(parseCallArguments(t, `{"version":2}`)), ErrArgumentsInvalid)
	require.NoError(t, second.Validator.Validate(parseCallArguments(t, `{"version":2}`)))
	require.ErrorIs(t, second.Validator.Validate(parseCallArguments(t, `{"version":1}`)), ErrArgumentsInvalid)

	assert.True(t, registry.MarkStaleExact(server.ID, "runtime-2", 2, 1))
	_, ok = registry.Routes().ResolveCall("sample.echo")
	assert.False(t, ok, "stale active catalogs are not future call targets")
	_, acquireErr := second.Capability.Acquire(context.Background())
	var rejection *downstream.PreStartRejection
	require.ErrorAs(t, acquireErr, &rejection)
	assert.Equal(t, downstream.RejectionStale, rejection.Reason)

	assert.True(t, registry.WithdrawExact(server.ID, "runtime-2", 2, contract.ActiveCatalogUnavailable))
	_, ok = registry.Routes().ResolveCall("sample.echo")
	assert.False(t, ok)
	registry.Drain()
	_, ok = registry.Routes().ResolveCall("sample.echo")
	assert.False(t, ok)
}

func TestActivePublicationRejectsExternalNameCollisionBeforeDurableMutation(t *testing.T) {
	repository, serverRepository, _, _ := newCatalogRepository(t)
	serverA := createCatalogServer(t, serverRepository, "alpha")
	serverB := createCatalogServer(t, serverRepository, "beta")
	registry, err := NewActiveRegistry(repository, &catalogClock{now: catalogTime}, activeProcessID)
	require.NoError(t, err)
	runtimeA, _ := newRouteRuntime(t)
	runtimeB, _ := newRouteRuntime(t)
	toolA := normalizeCallTool(t, serverA.ID, "shared.echo", `{"name":"echo","inputSchema":{"type":"object"}}`)
	_, err = registry.Publish(context.Background(), Publication{
		Fence: catalogFence(serverA.ID, "0"), RuntimeID: "runtime-a", RuntimeGeneration: 1,
		Candidate: NormalizedCandidate{Tools: []NormalizedTool{toolA}, RawCount: 1, Pages: 1}, Current: func() bool { return true }, Runtime: runtimeA,
	})
	require.NoError(t, err)
	toolB := normalizeCallTool(t, serverB.ID, "shared.echo", `{"name":"echo","inputSchema":{"type":"object"}}`)
	_, err = registry.Publish(context.Background(), Publication{
		Fence: catalogFence(serverB.ID, "0"), RuntimeID: "runtime-b", RuntimeGeneration: 1,
		Candidate: NormalizedCandidate{Tools: []NormalizedTool{toolB}, RawCount: 1, Pages: 1}, Current: func() bool { return true }, Runtime: runtimeB,
	})
	require.Error(t, err)
	status, statusErr := repository.Status(context.Background(), serverB.ID)
	require.NoError(t, statusErr)
	assert.Nil(t, status.Revision, "collision must be rejected before durable publication")
	target, ok := registry.Routes().ResolveCall("shared.echo")
	require.True(t, ok)
	assert.Equal(t, serverA.ID, target.ServerID)
}

func normalizeCallTool(t *testing.T, serverID, externalName, descriptor string) NormalizedTool {
	t.Helper()
	var object struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(descriptor), &object))
	normalized, err := NormalizeTool(RawTool{UpstreamName: object.Name, ExternalName: externalName, Descriptor: json.RawMessage(descriptor)}, NormalizeOptions{ServerID: serverID, AllowHeaderBindings: true})
	require.NoError(t, err)
	return normalized
}

func parseCallArguments(t *testing.T, arguments string) strictjson.Value {
	t.Helper()
	value, err := strictjson.ParseValue([]byte(arguments), strictjson.Options{MaxBytes: 1024, MaxDepth: 8})
	require.NoError(t, err)
	return value
}
