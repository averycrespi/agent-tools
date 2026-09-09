package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyInvocationBlocksWithoutDispatch(t *testing.T) {
	_, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	grants, err := authority.ListGrants(t.Context(), authorization.GrantFilter{PrincipalID: principal.ID}, nil, 100)
	require.NoError(t, err)
	for _, grant := range grants.Items {
		require.NoError(t, authority.DeleteGrant(t.Context(), grant.ID))
	}
	_, err = authority.CreateGrant(t.Context(), authorization.CreateGrantRequest{PrincipalID: principal.ID, ServerID: contract.SyntheticServerID, Effect: contract.GrantAllow, ReadOnly: true}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	acquisitions, executions := 0, 0
	hint := false
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		target := serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			acquisitions++
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				executions++
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		})
		target.readOnlyHint = hint
		return target, true
	})
	require.NoError(t, err)
	for _, eligible := range []bool{false, true, false} {
		hint = eligible
		lease, err := authority.Authenticate(t.Context(), credential.Bearer)
		require.NoError(t, err)
		before := acquisitions
		response := service.Call(t.Context(), lease, validCallParams())
		lease.Release()
		if eligible {
			require.NotNil(t, response.Result)
			require.Equal(t, before+1, acquisitions)
		} else {
			require.Equal(t, contract.RejectionBlock, response.RejectionReason)
			require.Equal(t, before, acquisitions)
		}
	}
	require.Equal(t, 1, executions)
}
