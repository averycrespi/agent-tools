package discovery

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyntheticDiscoveryObeysOrdinaryVisibilityAndDenyPrecedence(t *testing.T) {
	identity := "get_identity"
	create := "create_grant_request"
	allNames := []string{
		"mcp_gateway.cancel_grant_request", "mcp_gateway.create_grant_request", "mcp_gateway.get_grant_request",
		"mcp_gateway.get_identity", "mcp_gateway.list_grant_requests", "mcp_gateway.list_grants",
	}
	tests := []struct {
		name       string
		visibility contract.PrincipalVisibility
		grants     []authorization.StructuralGrant
		want       []string
	}{
		{name: "all", visibility: contract.VisibilityAll, want: allNames},
		{name: "requestable", visibility: contract.VisibilityRequestable, want: allNames},
		{name: "requestable exact deny", visibility: contract.VisibilityRequestable, grants: []authorization.StructuralGrant{{Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &identity}}, want: []string{"mcp_gateway.cancel_grant_request", "mcp_gateway.create_grant_request", "mcp_gateway.get_grant_request", "mcp_gateway.list_grant_requests", "mcp_gateway.list_grants"}},
		{name: "requestable constrained deny", visibility: contract.VisibilityRequestable, grants: []authorization.StructuralGrant{{Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &identity, Constrained: true}}, want: allNames},
		{name: "allowed only without grant", visibility: contract.VisibilityAllowedOnly, want: []string{}},
		{name: "allowed only server grant", visibility: contract.VisibilityAllowedOnly, grants: []authorization.StructuralGrant{{Effect: contract.GrantAllow, ServerID: contract.SyntheticServerID}}, want: allNames},
		{name: "allowed only exact grant", visibility: contract.VisibilityAllowedOnly, grants: []authorization.StructuralGrant{{Effect: contract.GrantAllow, ServerID: contract.SyntheticServerID, UpstreamName: &create}}, want: []string{"mcp_gateway.create_grant_request"}},
		{name: "server deny wins", visibility: contract.VisibilityAllowedOnly, grants: []authorization.StructuralGrant{{Effect: contract.GrantAllow, ServerID: contract.SyntheticServerID}, {Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID}}, want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := &fakePolicySource{view: policyView(test.visibility, test.grants)}
			catalogs := &fakeCatalogSource{snapshot: currentSnapshot(nil), current: true}
			service, err := NewWithSyntheticCatalog(policy, catalogs, projectionClock{now: projectionNow})
			require.NoError(t, err)
			result, err := service.Project(context.Background(), Request{})
			require.NoError(t, err)
			assert.Equal(t, test.want, toolNames(result.Tools))
			assert.Equal(t, currentSnapshot(nil).Generation, result.Snapshot.Generation)
		})
	}
}

func TestSyntheticDiscoveryMergesOrdersClonesAndPinsOnlyActiveGeneration(t *testing.T) {
	active := descriptor(opaqueID(11), opaqueID(1), "z-last", "z.last")
	policy := &fakePolicySource{view: policyView(contract.VisibilityAll, nil)}
	catalogs := &fakeCatalogSource{snapshot: currentSnapshot([]contract.ToolDescriptor{active}), current: true}
	service, err := NewWithSyntheticCatalog(policy, catalogs, projectionClock{now: projectionNow})
	require.NoError(t, err)
	first, err := service.Project(context.Background(), Request{})
	require.NoError(t, err)
	require.Len(t, first.Tools, 7)
	assert.Equal(t, "z.last", first.Tools[6].Name)
	first.Tools[0].Name = "mutated"
	first.Tools[0].InputSchema[0] = '['
	second, err := service.Project(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "mcp_gateway.cancel_grant_request", second.Tools[0].Name)
	assert.Equal(t, byte('{'), second.Tools[0].InputSchema[0])

	continuation := second.Snapshot
	catalogs.snapshot.Generation.ActiveGeneration++
	_, err = service.Project(context.Background(), Request{Continuation: &continuation})
	assert.ErrorIs(t, err, ErrStaleCursor)
}

func TestSyntheticPaginationReachesAll2054ToolsAndPositionsAbove2048(t *testing.T) {
	descriptors := make([]contract.ToolDescriptor, 2048)
	for index := range descriptors {
		prefix := "a"
		if index >= 2002 {
			prefix = "z"
		}
		name := fmt.Sprintf("%s%04d", prefix, index)
		descriptors[index] = descriptor(fmt.Sprintf("%026d", index+100), opaqueID(1), name, prefix+"."+name)
		if index >= 2002 {
			descriptors[index].Descriptor.InputSchema = []byte(`{"type":"object","description":"` + strings.Repeat("x", 90*1024) + `"}`)
		}
	}
	policy := &fakePolicySource{view: policyView(contract.VisibilityAll, nil)}
	catalogs := &fakeCatalogSource{snapshot: currentSnapshot(descriptors), current: true}
	catalogs.snapshot.Generation.ProcessGeneration = opaqueID(3)
	service, err := NewWithSyntheticCatalog(policy, catalogs, projectionClock{now: projectionNow})
	require.NoError(t, err)
	codec := mustCursorCodec(t, 0x5a)
	pager := mustPager(t, service, codec)
	cursor := ""
	seen := make([]string, 0, 2054)
	maximumPosition := uint32(0)
	for {
		page, listErr := pager.List(context.Background(), PageRequest{Cursor: cursor, Encode: (&countingEncoder{}).encode})
		require.NoError(t, listErr)
		seen = append(seen, toolNames(page.Result.Tools)...)
		cursor = page.Result.NextCursor
		if cursor == "" {
			break
		}
		state, decodeErr := codec.Decode(cursor, CursorMethodToolsList)
		require.NoError(t, decodeErr)
		maximumPosition = max(maximumPosition, state.Position)
	}
	require.Len(t, seen, 2054)
	assert.Greater(t, maximumPosition, uint32(2048))
	assert.Equal(t, "a.a0000", seen[0])
	assert.Equal(t, "mcp_gateway.cancel_grant_request", seen[2002])
	assert.Equal(t, "mcp_gateway.list_grants", seen[2007])
	assert.Equal(t, "z.z2047", seen[len(seen)-1])
}
