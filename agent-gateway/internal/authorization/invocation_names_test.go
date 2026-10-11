package authorization

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestInvocationGrantRecognitionTracksDescriptionsAndAbsence(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	grant, err := repository.CreateGrant(t.Context(), CreateGrantRequest{PrincipalID: principal.ID, Effect: contract.GrantAllow, Target: accesstarget.MCP{ServerID: id(51)}}, allowCurrentTarget)
	require.NoError(t, err)
	name, err := repository.InvocationGrantDescription(t.Context(), grant.ID)
	require.NoError(t, err)
	require.NotNil(t, name)
	require.Empty(t, *name, "existing unnamed grant differs from unavailable")
	description := "Renamed grant"
	value := &description
	_, err = repository.PatchGrant(t.Context(), grant.ID, PatchGrantRequest{ExpectedRevision: grant.Revision, Description: &value})
	require.NoError(t, err)
	name, err = repository.InvocationGrantDescription(t.Context(), grant.ID)
	require.NoError(t, err)
	require.Equal(t, &description, name)
	require.NoError(t, repository.DeleteGrant(t.Context(), grant.ID))
	name, err = repository.InvocationGrantDescription(t.Context(), grant.ID)
	require.NoError(t, err)
	require.Nil(t, name)
	name, err = repository.InvocationGrantDescription(t.Context(), id(99))
	require.NoError(t, err)
	require.Nil(t, name)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	name, err = repository.InvocationGrantDescription(ctx, grant.ID)
	require.Error(t, err)
	require.Nil(t, name)
}
