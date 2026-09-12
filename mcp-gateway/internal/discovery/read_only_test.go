package discovery

import (
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyDiscoveryEligibilityAndComposition(t *testing.T) {
	descriptor := contract.ToolDescriptor{ServerID: contract.SyntheticServerID, UpstreamName: "tool"}
	grants := []authorization.StructuralGrant{{Effect: contract.GrantAllow, ReadOnly: true, Target: accesstarget.MCP{ServerID: descriptor.ServerID}}}
	for _, hint := range []bool{false, true} {
		descriptor.Descriptor.Annotations.ReadOnlyHint = hint
		for _, visibility := range []contract.PrincipalVisibility{contract.VisibilityAllowedOnly, contract.VisibilityAll, contract.VisibilityRequestable} {
			visible, err := structurallyVisible(visibility, descriptor, grants)
			require.NoError(t, err)
			require.Equal(t, hint || visibility != contract.VisibilityAllowedOnly, visible)
		}
	}
	descriptor.Descriptor.Annotations.ReadOnlyHint = false
	grants = append(grants, authorization.StructuralGrant{Effect: contract.GrantAllow, Target: accesstarget.MCP{ServerID: descriptor.ServerID}})
	visible, err := structurallyVisible(contract.VisibilityAllowedOnly, descriptor, grants)
	require.NoError(t, err)
	require.True(t, visible)
	grants = append(grants, authorization.StructuralGrant{Effect: contract.GrantDeny, Target: accesstarget.MCP{ServerID: descriptor.ServerID}})
	visible, err = structurallyVisible(contract.VisibilityAllowedOnly, descriptor, grants)
	require.NoError(t, err)
	require.False(t, visible)
}
