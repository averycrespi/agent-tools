package authorization

import (
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestAccessTargetScopeAtGrantAndCallBoundaries(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, repository)
	target := accesstarget.Tool(contract.SyntheticServerID, "get_identity")
	result, err := repository.Evaluate(t.Context(), EvaluationRequest{
		PrincipalID: principal.ID, Target: target, Arguments: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.Equal(t, contract.DecisionAllow, result.Decision, "the ordinary default grant covers synthetic calls")

	for _, invalid := range []accesstarget.MCP{
		{},
		{ServerID: contract.SyntheticServerID},
		accesstarget.Tool(contract.SyntheticServerID, ""),
		accesstarget.Tool("invalid", "get_identity"),
		accesstarget.Tool(contract.SyntheticServerID, "invalid/name"),
	} {
		_, err := repository.Evaluate(t.Context(), EvaluationRequest{
			PrincipalID: principal.ID, Target: invalid, Arguments: json.RawMessage(`{}`),
		})
		require.ErrorIs(t, err, ErrInvalidInput)
		lease := mustAuthenticateLease(t, repository, credential.Bearer)
		_, pending, err := verifyResolvedMutation(repository, store, lease, ResolvedVerification{
			Target: invalid, Arguments: mustAdmissionArguments(t, `{}`),
		}, nil)
		require.ErrorIs(t, err, ErrInvalidInput)
		require.Nil(t, pending)
		lease.Release()
	}

	grant, err := repository.CreateGrant(t.Context(), CreateGrantRequest{
		PrincipalID: principal.ID, Target: accesstarget.MCP{ServerID: contract.SyntheticServerID}, Effect: contract.GrantDeny,
	}, allowCurrentTarget)
	require.NoError(t, err, "server scope remains valid for ordinary grants")
	require.Equal(t, contract.SyntheticServerID, grant.ServerID)
	require.Nil(t, grant.UpstreamName, "the SQL/public adapter retains nullable server scope")
	result, err = repository.Evaluate(t.Context(), EvaluationRequest{
		PrincipalID: principal.ID, Target: target, Arguments: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.Equal(t, contract.DecisionDeny, result.Decision)
}
