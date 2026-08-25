package discovery

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var projectionNow = time.Date(2026, 8, 25, 18, 0, 0, 0, time.UTC)

type projectionClock struct{ now time.Time }

func (clock projectionClock) Now() time.Time { return clock.now }

type fakePolicySource struct {
	view  authorization.DiscoveryPolicy
	err   error
	load  func(time.Time) authorization.DiscoveryPolicy
	calls int
	times []time.Time
}

func (source *fakePolicySource) LoadDiscoveryPolicy(_ context.Context, _ *authorization.Lease, evaluatedAt time.Time) (authorization.DiscoveryPolicy, error) {
	source.calls++
	source.times = append(source.times, evaluatedAt)
	view := source.view
	if source.load != nil {
		view = source.load(evaluatedAt)
	}
	view.EvaluatedAt = evaluatedAt
	return view, source.err
}

type fakeCatalogSource struct {
	snapshot catalog.CurrentSnapshot
	current  bool
	reads    int
	rechecks int
}

func (source *fakeCatalogSource) CurrentSnapshot() catalog.CurrentSnapshot {
	source.reads++
	return source.snapshot
}

func (source *fakeCatalogSource) IsCurrentGeneration(catalog.CurrentGeneration) bool {
	source.rechecks++
	return source.current
}

func TestProjectAppliesEveryStructuralVisibilityMode(t *testing.T) {
	serverOne, serverTwo := opaqueID(1), opaqueID(2)
	descriptors := []contract.ToolDescriptor{
		descriptor(opaqueID(13), serverTwo, "same", "beta.same"),
		descriptor(opaqueID(12), serverOne, "other", "alpha.other"),
		descriptor(opaqueID(11), serverOne, "same", "alpha.same"),
	}
	tool := "same"
	tests := []struct {
		name       string
		visibility contract.PrincipalVisibility
		grants     []authorization.StructuralGrant
		want       []string
	}{
		{name: "all without grants", visibility: contract.VisibilityAll, want: []string{"alpha.other", "alpha.same", "beta.same"}},
		{name: "requestable without grants", visibility: contract.VisibilityRequestable, want: []string{"alpha.other", "alpha.same", "beta.same"}},
		{name: "requestable excludes unconstrained deny", visibility: contract.VisibilityRequestable, grants: []authorization.StructuralGrant{{Effect: contract.GrantDeny, ServerID: serverOne, UpstreamName: &tool}}, want: []string{"alpha.other", "beta.same"}},
		{name: "requestable keeps constrained deny", visibility: contract.VisibilityRequestable, grants: []authorization.StructuralGrant{{Effect: contract.GrantDeny, ServerID: serverOne, UpstreamName: &tool, Constrained: true}}, want: []string{"alpha.other", "alpha.same", "beta.same"}},
		{name: "allowed only server allow", visibility: contract.VisibilityAllowedOnly, grants: []authorization.StructuralGrant{{Effect: contract.GrantAllow, ServerID: serverOne}}, want: []string{"alpha.other", "alpha.same"}},
		{name: "allowed only constrained exact allow", visibility: contract.VisibilityAllowedOnly, grants: []authorization.StructuralGrant{{Effect: contract.GrantAllow, ServerID: serverOne, UpstreamName: &tool, Constrained: true}}, want: []string{"alpha.same"}},
		{name: "allowed only deny wins structurally", visibility: contract.VisibilityAllowedOnly, grants: []authorization.StructuralGrant{{Effect: contract.GrantAllow, ServerID: serverOne}, {Effect: contract.GrantDeny, ServerID: serverOne, UpstreamName: &tool}}, want: []string{"alpha.other"}},
		{name: "allowed only constrained deny does not hide", visibility: contract.VisibilityAllowedOnly, grants: []authorization.StructuralGrant{{Effect: contract.GrantAllow, ServerID: serverOne}, {Effect: contract.GrantDeny, ServerID: serverOne, UpstreamName: &tool, Constrained: true}}, want: []string{"alpha.other", "alpha.same"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := &fakePolicySource{view: policyView(test.visibility, test.grants)}
			catalogs := &fakeCatalogSource{snapshot: currentSnapshot(descriptors), current: true}
			service := mustService(t, policy, catalogs)
			result, err := service.Project(context.Background(), Request{})
			require.NoError(t, err)
			assert.Equal(t, test.want, toolNames(result.Tools))
			assert.Equal(t, 1, policy.calls)
			assert.Equal(t, 1, catalogs.reads)
			assert.Equal(t, 1, catalogs.rechecks)
		})
	}
}

func TestProjectPinsViewAndReturnsOneAttemptMismatchErrors(t *testing.T) {
	policy := &fakePolicySource{view: policyView(contract.VisibilityAll, nil)}
	catalogs := &fakeCatalogSource{snapshot: currentSnapshot([]contract.ToolDescriptor{descriptor(opaqueID(11), opaqueID(1), "tool", "ns.tool")}), current: false}
	service := mustService(t, policy, catalogs)

	_, err := service.Project(context.Background(), Request{})
	assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
	assert.Equal(t, 1, policy.calls)
	assert.Equal(t, 1, catalogs.reads)
	assert.Equal(t, 1, catalogs.rechecks)

	policy.calls, catalogs.reads, catalogs.rechecks = 0, 0, 0
	continuation := snapshotEvidence(policy.view, catalogs.snapshot.Generation, projectionNow)
	_, err = service.Project(context.Background(), Request{Continuation: &continuation})
	assert.ErrorIs(t, err, ErrStaleCursor)
	assert.Equal(t, []time.Time{projectionNow, projectionNow}, policy.times)
	assert.Equal(t, 1, policy.calls)
	assert.Equal(t, 1, catalogs.reads)
	assert.Equal(t, 1, catalogs.rechecks)

	catalogs.current = true
	for _, mutate := range []func(*Snapshot){
		func(snapshot *Snapshot) { snapshot.AuthorizationRevision = "999" },
		func(snapshot *Snapshot) { snapshot.PrincipalRevision = "999" },
		func(snapshot *Snapshot) { snapshot.CredentialRevision = "999" },
		func(snapshot *Snapshot) { snapshot.Visibility = contract.VisibilityAllowedOnly },
	} {
		changed := continuation
		mutate(&changed)
		_, err = service.Project(context.Background(), Request{Continuation: &changed})
		assert.ErrorIs(t, err, ErrStaleCursor)
	}
}

func TestProjectContinuationPinsEvaluationTimeAcrossExpiry(t *testing.T) {
	serverID := opaqueID(1)
	base := policyView(contract.VisibilityAllowedOnly, nil)
	policy := &fakePolicySource{load: func(evaluatedAt time.Time) authorization.DiscoveryPolicy {
		view := base
		if evaluatedAt.Before(projectionNow) {
			view.Grants = []authorization.StructuralGrant{{Effect: contract.GrantAllow, ServerID: serverID}}
		}
		return view
	}}
	catalogs := &fakeCatalogSource{snapshot: currentSnapshot([]contract.ToolDescriptor{descriptor(opaqueID(11), serverID, "tool", "ns.tool")}), current: true}
	service := mustService(t, policy, catalogs)
	first, err := service.Project(context.Background(), Request{})
	require.NoError(t, err)
	assert.Empty(t, first.Tools)

	pinnedTime := projectionNow.Add(-time.Nanosecond)
	pinned := snapshotEvidence(base, catalogs.snapshot.Generation, pinnedTime)
	continuation, err := service.Project(context.Background(), Request{Continuation: &pinned})
	require.NoError(t, err)
	assert.Equal(t, []string{"ns.tool"}, toolNames(continuation.Tools))
	assert.Equal(t, []time.Time{projectionNow, pinnedTime}, policy.times)
}

func TestProjectCancellationAndPolicyFaultsFailWithoutCatalogAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policy := &fakePolicySource{view: policyView(contract.VisibilityAll, nil)}
	catalogs := &fakeCatalogSource{snapshot: currentSnapshot(nil), current: true}
	service := mustService(t, policy, catalogs)
	_, err := service.Project(ctx, Request{})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, policy.calls)
	assert.Zero(t, catalogs.reads)

	policy.err = authorization.ErrStorageUnavailable
	_, err = service.Project(context.Background(), Request{})
	assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
	assert.Zero(t, catalogs.reads)
	policy.err = authorization.ErrAuthenticationRequired
	_, err = service.Project(context.Background(), Request{})
	assert.ErrorIs(t, err, authorization.ErrAuthenticationRequired)
}

func TestProjectExactMCPFieldClosureAndCloneIsolation(t *testing.T) {
	title, description, annotationTitle := "Title", "Description", "Annotation"
	descriptorValue := descriptor(opaqueID(11), opaqueID(1), "upstream", "ns.external")
	descriptorValue.Descriptor.Title = &title
	descriptorValue.Descriptor.Description = &description
	descriptorValue.Descriptor.InputSchema = json.RawMessage(`{"type":"object","properties":{"x":{"type":"string","x-mcp-header":{"name":"X-Test"}}}}`)
	descriptorValue.Descriptor.OutputSchema = json.RawMessage(`{"type":"object"}`)
	descriptorValue.Descriptor.Annotations = contract.NormalizedToolAnnotations{
		Title: &annotationTitle, ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false,
	}
	policy := &fakePolicySource{view: policyView(contract.VisibilityAll, nil)}
	catalogs := &fakeCatalogSource{snapshot: currentSnapshot([]contract.ToolDescriptor{descriptorValue}), current: true}
	result, err := mustService(t, policy, catalogs).Project(context.Background(), Request{})
	require.NoError(t, err)
	require.Len(t, result.Tools, 1)
	tool := result.Tools[0]
	assert.Equal(t, "ns.external", tool.Name)
	assert.Equal(t, title, tool.Title)
	assert.Equal(t, description, tool.Description)
	assert.Equal(t, []string{"Annotations", "Description", "InputSchema", "Name", "OutputSchema", "Title"}, exportedFields(reflect.TypeOf(Tool{})))
	require.NotNil(t, tool.Annotations)
	assert.Equal(t, annotationTitle, tool.Annotations.Title)
	assert.Equal(t, true, tool.Annotations.ReadOnlyHint)
	assert.Equal(t, true, tool.Annotations.IdempotentHint)
	require.NotNil(t, tool.Annotations.DestructiveHint)
	require.NotNil(t, tool.Annotations.OpenWorldHint)
	assert.False(t, *tool.Annotations.DestructiveHint)
	assert.False(t, *tool.Annotations.OpenWorldHint)

	encoded, err := json.Marshal(tool)
	require.NoError(t, err)
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &object))
	assert.ElementsMatch(t, []string{"name", "title", "description", "inputSchema", "outputSchema", "annotations"}, mapKeys(object))
	assert.NotContains(t, string(encoded), descriptorValue.ID)
	assert.NotContains(t, string(encoded), descriptorValue.ServerID)
	assert.NotContains(t, string(encoded), descriptorValue.UpstreamName)

	tool.InputSchema[0] = '['
	tool.OutputSchema[0] = '['
	assert.Equal(t, byte('{'), descriptorValue.Descriptor.InputSchema[0])
	assert.Equal(t, byte('{'), descriptorValue.Descriptor.OutputSchema[0])
	assert.Equal(t, []string{"Generation", "PrincipalID", "PrincipalRevision", "Visibility", "CredentialID", "CredentialRevision", "AuthorizationRevision", "EvaluatedAt"}, exportedFields(reflect.TypeOf(result.Snapshot)))
}

func mustService(t *testing.T, policy PolicySource, catalogs CatalogSource) *Service {
	t.Helper()
	service, err := New(policy, catalogs, projectionClock{now: projectionNow})
	require.NoError(t, err)
	return service
}

func policyView(visibility contract.PrincipalVisibility, grants []authorization.StructuralGrant) authorization.DiscoveryPolicy {
	return authorization.DiscoveryPolicy{
		Binding: authorization.CredentialBinding{
			PrincipalID: opaqueID(91), PrincipalRevision: "3", Visibility: visibility,
			CredentialID: opaqueID(92), CredentialRevision: "4", CredentialFingerprint: "0123456789abcdef",
		},
		AuthorizationRevision: "7",
		Grants:                grants,
	}
}

func currentSnapshot(descriptors []contract.ToolDescriptor) catalog.CurrentSnapshot {
	return catalog.CurrentSnapshot{
		Generation:  catalog.CurrentGeneration{ProcessGeneration: "process", ActiveGeneration: 8},
		Descriptors: descriptors,
	}
}

func snapshotEvidence(policy authorization.DiscoveryPolicy, generation catalog.CurrentGeneration, evaluatedAt time.Time) Snapshot {
	return Snapshot{
		Generation: generation, PrincipalID: policy.Binding.PrincipalID,
		PrincipalRevision: policy.Binding.PrincipalRevision, Visibility: policy.Binding.Visibility,
		CredentialID: policy.Binding.CredentialID, CredentialRevision: policy.Binding.CredentialRevision,
		AuthorizationRevision: policy.AuthorizationRevision, EvaluatedAt: evaluatedAt,
	}
}

func descriptor(toolID, serverID, upstreamName, externalName string) contract.ToolDescriptor {
	return contract.ToolDescriptor{
		ID: toolID, ServerID: serverID, UpstreamName: upstreamName, ExternalName: externalName,
		Descriptor: contract.NormalizedToolDescriptor{
			Name: upstreamName, InputSchema: json.RawMessage(`{"type":"object"}`),
			Annotations: contract.NormalizedToolAnnotations{DestructiveHint: true, OpenWorldHint: true},
		},
	}
}

func opaqueID(value byte) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	return "01ARZ3NDEKTSV4RRFFQ69G5FA" + string(alphabet[value%32])
}

func toolNames(tools []*Tool) []string {
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Name
	}
	return names
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func exportedFields(typ reflect.Type) []string {
	fields := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		if typ.Field(index).IsExported() {
			fields = append(fields, typ.Field(index).Name)
		}
	}
	return fields
}
