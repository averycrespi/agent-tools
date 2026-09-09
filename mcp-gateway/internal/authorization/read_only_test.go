package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyGrantEligibilityCompositionAndValidation(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	request := CreateGrantRequest{PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), ReadOnly: true}
	grant := mustCreateEvaluationGrant(t, repository, request)
	require.True(t, grant.ReadOnly)
	loaded, err := repository.GetGrant(t.Context(), grant.ID)
	require.NoError(t, err)
	require.True(t, loaded.ReadOnly)
	evaluate := func(hint bool, want contract.AuthorizationDecision) {
		t.Helper()
		require.NoError(t, repository.view(t.Context(), func(tx *sql.Tx) error {
			result, err := evaluateTx(repository, t.Context(), tx, principal.ID, id(51), "tool", strictjson.Value{Type: strictjson.ValueObject}, testNow, hint)
			require.NoError(t, err)
			require.Equal(t, want, result.Decision)
			return nil
		}))
	}
	evaluate(false, contract.DecisionBlock)
	evaluate(true, contract.DecisionAllow)
	result, err := repository.Evaluate(t.Context(), EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "tool", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.Equal(t, contract.DecisionBlock, result.Decision, "unbound evaluation has no descriptor authority")
	unrestricted := request
	unrestricted.ReadOnly = false
	unrestricted.UpstreamName = stringPointer("tool")
	writeGrant := mustCreateEvaluationGrant(t, repository, unrestricted)
	evaluate(false, contract.DecisionAllow)
	deny := unrestricted
	deny.Effect = contract.GrantDeny
	denied := mustCreateEvaluationGrant(t, repository, deny)
	evaluate(true, contract.DecisionDeny)
	evaluate(false, contract.DecisionDeny)
	require.NoError(t, repository.DeleteGrant(t.Context(), denied.ID))
	require.NoError(t, repository.DeleteGrant(t.Context(), writeGrant.ID))
	require.NoError(t, repository.DeleteGrant(t.Context(), grant.ID))
	evaluate(true, contract.DecisionBlock)
	for _, invalid := range []CreateGrantRequest{
		{PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: id(51), ReadOnly: true},
		{PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: stringPointer("tool"), ReadOnly: true},
		{PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), Constraint: rawPointer(`{"equals":{"/x":1}}`), ReadOnly: true},
	} {
		_, err := repository.CreateGrant(t.Context(), invalid, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
		require.ErrorIs(t, err, ErrInvalidInput)
	}
}

func rawPointer(value string) *json.RawMessage { raw := json.RawMessage(value); return &raw }
