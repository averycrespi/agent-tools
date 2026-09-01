package grantrequests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

func TestGrantRequestAdminSummaryItemCursorAndCurrentComparison(t *testing.T) {
	active := &fakeActiveTargetInspector{state: contract.TargetActiveCurrent}
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	descriptors := &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{
		"sample.echo": requestDescriptor(t, requestID(400), requestID(401), "sample", "echo", contract.EvidenceCurrent),
	}}
	repository, _ := newRequestRepository(t, requestRepositoryOptions{
		namespaces: namespaces, descriptors: descriptors, active: active,
	})
	exact, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyTool, Target: "sample.echo",
	}})
	require.NoError(t, err)
	server, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(201), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)

	page, err := repository.ListAdmin(context.Background(), AdminFilter{}, nil, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.Next)
	contents, err := json.Marshal(page.Items[0])
	require.NoError(t, err)
	assert.NotContains(t, string(contents), "evidence")
	assert.NotContains(t, string(contents), "resolved_server_id")
	continued, err := repository.ListAdmin(context.Background(), AdminFilter{}, page.Next, 2)
	require.NoError(t, err)
	require.Len(t, continued.Items, 1)
	assert.Nil(t, continued.Next)
	state := contract.RequestPending
	filtered, err := repository.ListAdmin(context.Background(), AdminFilter{PrincipalID: requestID(201), State: &state}, nil, 10)
	require.NoError(t, err)
	require.Len(t, filtered.Items, 1)
	assert.Equal(t, server.Request.ID, filtered.Items[0].ID)
	_, err = repository.ListAdmin(context.Background(), AdminFilter{PrincipalID: requestID(201)}, page.Next, 10)
	assert.ErrorIs(t, err, ErrStaleCursor)

	item, err := repository.GetAdmin(context.Background(), exact.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, requestID(200), item.PrincipalID)
	assert.Equal(t, requestID(400), item.ResolvedServerID)
	require.NotNil(t, item.SubmittedEvidence)
	assert.Nil(t, item.ApprovedEvidence)
	assert.Equal(t, contract.PolicyTool, item.CurrentTarget.Scope)
	assert.Equal(t, contract.TargetExtant, item.CurrentTarget.TargetState)
	require.NotNil(t, item.CurrentTarget.ActiveState)
	assert.Equal(t, contract.TargetActiveCurrent, *item.CurrentTarget.ActiveState)
	require.NotNil(t, item.CurrentTarget.DurableState)
	assert.Equal(t, contract.TargetDurableCurrent, *item.CurrentTarget.DurableState)
	require.NotNil(t, item.CurrentTarget.Descriptor)

	namespaces.targets["sample"] = servers.NamespaceTarget{ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDeleted}
	descriptors.descriptors["sample.echo"] = requestDescriptor(t, requestID(400), requestID(401), "sample", "echo", contract.EvidenceRetired)
	active.state = contract.TargetActiveStale
	changed, err := repository.GetAdmin(context.Background(), exact.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.TargetDeleted, changed.CurrentTarget.TargetState)
	assert.Equal(t, contract.TargetActiveStale, *changed.CurrentTarget.ActiveState)
	assert.Equal(t, contract.TargetDurableRetired, *changed.CurrentTarget.DurableState)
	delete(descriptors.descriptors, "sample.echo")
	active.state = contract.TargetActiveAbsent
	absent, err := repository.GetAdmin(context.Background(), exact.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.TargetActiveAbsent, *absent.CurrentTarget.ActiveState)
	assert.Equal(t, contract.TargetDurableAbsent, *absent.CurrentTarget.DurableState)
	assert.Nil(t, absent.CurrentTarget.Descriptor)

	serverItem, err := repository.GetAdmin(context.Background(), server.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.PolicyServer, serverItem.CurrentTarget.Scope)
	assert.Nil(t, serverItem.CurrentTarget.ActiveState)
	assert.Nil(t, serverItem.CurrentTarget.DurableState)
	assert.Nil(t, serverItem.CurrentTarget.Descriptor)
	_, err = repository.GetAdmin(context.Background(), requestID(999))
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestGrantRequestAdminServiceReturnsFullAdjudicationWithoutFollowupRowRead(t *testing.T) {
	fixture := newApprovalFixture(t)
	fixture.requests.active = &fakeActiveTargetInspector{state: contract.TargetActiveCurrent}
	fixture.descriptors.descriptors["sample.tool"] = requestDescriptor(t, requestID(400), requestID(500), "sample", "tool", contract.EvidenceCurrent)
	service, err := NewAdminService(fixture.requests, fixture.authority)
	require.NoError(t, err)
	created := fixture.createRequest(t, serverApprovalPolicy())
	fixture.clock.now = requestTestTime.Add(time.Minute)
	fixture.invalidations = nil
	description := "Approved access"
	approved, err := service.ApproveAdmin(context.Background(), created.ID, "1", &description, contract.Policy{
		Scope: contract.PolicyTool, Target: "sample.tool",
	})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestApproved, approved.State)
	require.NotNil(t, approved.ApprovedEvidence)
	assert.Equal(t, contract.PolicyTool, approved.CurrentTarget.Scope)
	assert.Equal(t, contract.TargetActiveCurrent, *approved.CurrentTarget.ActiveState)
	assert.Equal(t, []contract.Invalidation{
		{Kind: contract.InvalidationGrantRequests}, {Kind: contract.InvalidationAuthorization},
	}, fixture.invalidations)

	second := fixture.createRequest(t, serverApprovalPolicy())
	fixture.clock.now = requestTestTime.Add(2 * time.Minute)
	fixture.invalidations = nil
	rejected, err := service.RejectAdmin(context.Background(), second.ID, "1", contract.RejectionNotApproved)
	require.NoError(t, err)
	assert.Equal(t, contract.RequestRejected, rejected.State)
	assert.Equal(t, []contract.Invalidation{{Kind: contract.InvalidationGrantRequests}}, fixture.invalidations)
}

type fakeActiveTargetInspector struct {
	state contract.TargetActiveState
}

func (inspector *fakeActiveTargetInspector) CompareActiveTarget(context.Context, string, string, string) contract.TargetActiveState {
	return inspector.state
}
