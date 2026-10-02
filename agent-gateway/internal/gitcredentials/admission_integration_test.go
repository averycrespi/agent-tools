//go:build integration

package gitcredentials

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitCredentialMutationFencesPendingAdmission(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	principal, err := s.authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	agent, err := s.authority.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	created, err := s.Create(ctx, definition(), []byte("pending-private-canary"))
	require.NoError(t, err)
	lease, err := s.authority.Authenticate(ctx, agent.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	evaluation, err := s.authority.EvaluateAdmission(ctx, lease, installation, &authorization.ResolvedVerification{Target: accesstarget.Tool(contract.SyntheticServerID, "tool"), Arguments: strictjson.Value{Type: strictjson.ValueObject}})
	require.NoError(t, err)
	require.NotNil(t, evaluation.Candidate)
	_, err = s.Rotate(ctx, created.ID, created.Revision, []byte("rotated-pending-private-canary"))
	require.NoError(t, err)
	_, err = s.authority.ConfirmEvaluation(ctx, evaluation.Candidate, installation, func(contract.AuthorizationResult, func() bool) bool {
		t.Fatal("older pending admission confirmed after credential rotation")
		return false
	})
	require.Error(t, err)
}
