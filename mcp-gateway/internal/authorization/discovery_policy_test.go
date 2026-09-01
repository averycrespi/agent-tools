package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDiscoveryPolicyReturnsOneCredentialBoundStructuralView(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
	require.NoError(t, err)
	lease, err := repository.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	t.Cleanup(lease.Release)

	tool := "tool"
	constraint := json.RawMessage(`{"equals":{"/tenant":"one"}}`)
	expires := testNow.Add(time.Hour)
	_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Name: "Test grant",
		PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51),
	}, allowCurrentTarget)
	require.NoError(t, err)
	_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Name: "Test grant",
		PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: id(52),
		UpstreamName: &tool, Constraint: &constraint, ExpiresAt: &expires,
	}, allowCurrentTarget)
	require.NoError(t, err)

	view, err := repository.LoadDiscoveryPolicy(context.Background(), lease, testNow)
	require.NoError(t, err)
	revision, err := repository.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, lease.Binding(), view.Binding)
	assert.Equal(t, revision, view.AuthorizationRevision)
	assert.Equal(t, testNow, view.EvaluatedAt)
	assert.Equal(t, []StructuralGrant{
		{Effect: contract.GrantAllow, ServerID: contract.SyntheticServerID},
		{Effect: contract.GrantAllow, ServerID: id(51)},
		{Effect: contract.GrantDeny, ServerID: id(52), UpstreamName: &tool, Constrained: true},
	}, view.Grants)
}

func TestLoadDiscoveryPolicyAppliesExpiryAtCallerTimestampAndFailsClosed(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
	require.NoError(t, err)
	lease, err := repository.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)

	expires := testNow.Add(time.Nanosecond)
	tool := "tool"
	constraint := json.RawMessage(`{"equals":{"/x":"value"}}`)
	_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Name: "Test grant",
		PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: &tool,
		Constraint: &constraint, ExpiresAt: &expires,
	}, allowCurrentTarget)
	require.NoError(t, err)
	before, err := repository.LoadDiscoveryPolicy(context.Background(), lease, testNow)
	require.NoError(t, err)
	assert.True(t, hasStructuralServer(before.Grants, id(51)))
	atExpiry, err := repository.LoadDiscoveryPolicy(context.Background(), lease, expires)
	require.NoError(t, err)
	assert.False(t, hasStructuralServer(atExpiry.Grants, id(51)))

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, updateErr := transaction.Exec(`UPDATE grants SET constraint_json = '{"equals":{"/x":[]}}' WHERE server_id = ?`, id(51))
		return updateErr
	}))
	view, err := repository.LoadDiscoveryPolicy(context.Background(), lease, testNow)
	assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
	assert.Empty(t, view.Grants)

	lease.Release()
	_, err = repository.LoadDiscoveryPolicy(context.Background(), lease, testNow)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	_, err = repository.LoadDiscoveryPolicy(context.Background(), nil, testNow)
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, err = repository.LoadDiscoveryPolicy(context.Background(), lease, time.Time{})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func hasStructuralServer(grants []StructuralGrant, serverID string) bool {
	for _, grant := range grants {
		if grant.ServerID == serverID {
			return true
		}
	}
	return false
}
