package grantrequests

import (
	"context"
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyRequestDedupeApprovalAndDurableReads(t *testing.T) {
	fixture := newApprovalFixture(t)
	policy := contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true, ReadOnly: true}
	created := fixture.createRequest(t, policy)
	repeated, err := fixture.requests.CreateOrExisting(t.Context(), CreateRequest{PrincipalID: fixture.principal.Principal.ID, Policy: policy})
	require.NoError(t, err)
	require.NotNil(t, repeated.Request)
	require.Equal(t, created.ID, repeated.Request.ID)
	require.True(t, repeated.Request.RequestedPolicy.ReadOnly)
	unrestricted := policy
	unrestricted.ReadOnly = false
	other := fixture.createRequest(t, unrestricted)
	require.NotEqual(t, created.ID, other.ID)
	for _, broadened := range []contract.Policy{unrestricted, {Scope: contract.PolicyTool, Target: "sample.tool"}} {
		_, err = fixture.requests.Approve(t.Context(), fixture.authority, ApproveRequest{ID: created.ID, ExpectedRevision: "1", ApprovedPolicy: broadened})
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	approved, err := fixture.requests.Approve(t.Context(), fixture.authority, ApproveRequest{ID: created.ID, ExpectedRevision: "1", ApprovedPolicy: policy})
	require.NoError(t, err)
	require.True(t, approved.Grant.ReadOnly)
	require.NotNil(t, approved.Request.ApprovedPolicy)
	require.True(t, approved.Request.ApprovedPolicy.ReadOnly)
	grant, err := fixture.authority.GetGrant(t.Context(), approved.Grant.ID)
	require.NoError(t, err)
	require.True(t, grant.ReadOnly)
	admin, err := fixture.requests.GetAdmin(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, admin.RequestedPolicy.ReadOnly)
	require.True(t, admin.ApprovedPolicy.ReadOnly)
	owned, found, err := fixture.requests.GetOwned(t.Context(), fixture.principal.Principal.ID, created.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, owned.ApprovedPolicy.ReadOnly)
	narrowed, err := fixture.requests.Approve(t.Context(), fixture.authority, ApproveRequest{ID: other.ID, ExpectedRevision: "1", ApprovedPolicy: policy})
	require.NoError(t, err)
	require.True(t, narrowed.Grant.ReadOnly)
	require.NoError(t, fixture.requests.ValidateStartup(t.Context(), fixture.authority, &fakeStoredTargetInspector{namespaces: map[string]string{requestID(400): "sample"}}))
}

func TestReadOnlyRequestConservativeWriteDenyConflict(t *testing.T) {
	fixture := newApprovalFixture(t)
	fixture.requests.denies = fixture.authority
	policy := contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true, ReadOnly: true}
	created := fixture.createRequest(t, policy)
	_, err := fixture.authority.CreateGrant(t.Context(), authorization.CreateGrantRequest{PrincipalID: fixture.principal.Principal.ID, Effect: contract.GrantDeny, ServerID: requestID(400), UpstreamName: stringPointer("write")}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	_, err = fixture.requests.Approve(t.Context(), fixture.authority, ApproveRequest{ID: created.ID, ExpectedRevision: "1", ApprovedPolicy: policy})
	require.ErrorIs(t, err, ErrConflict)
	pending, found, err := fixture.requests.GetOwned(t.Context(), fixture.principal.Principal.ID, created.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contract.RequestPending, pending.State)
	require.Nil(t, pending.ApprovedGrantID)
	policy.DurationSeconds = stringPointer("60")
	blocked, err := fixture.requests.CreateOrExisting(t.Context(), CreateRequest{PrincipalID: fixture.principal.Principal.ID, Policy: policy})
	require.NoError(t, err)
	require.Equal(t, contract.RequestDenyConflict, blocked.Outcome)
}

func TestReadOnlyPolicyDedupeVersionAndNarrowing(t *testing.T) {
	policy := contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true}
	legacy, err := CompilePolicy(policy)
	require.NoError(t, err)
	target := ResolvedTarget{ServerID: requestID(400)}
	old, err := CanonicalDedupeIdentity(legacy, target)
	require.NoError(t, err)
	require.Equal(t, DedupeVersionV1, old.Version)
	policy.ReadOnly = true
	restricted, err := CompilePolicy(policy)
	require.NoError(t, err)
	identity, err := CanonicalDedupeIdentity(restricted, target)
	require.NoError(t, err)
	require.Equal(t, DedupeVersionV3, identity.Version)
	require.NotEqual(t, old.Bytes, identity.Bytes)
	require.NoError(t, ValidateNarrowing(legacy, target, restricted, target))
	require.ErrorIs(t, ValidateNarrowing(restricted, target, legacy, target), ErrPolicyBroadening)
	policy.Scope = contract.PolicyTool
	policy.Target = "sample.tool"
	policy.FutureToolsAcknowledged = false
	_, err = CompilePolicy(policy)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}
