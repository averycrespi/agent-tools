package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

func TestS5ApprovalAuthorityGateIsNonQueueing(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{
		DisplayName: "Approval owner", Visibility: contract.VisibilityRequestable,
	})
	require.NoError(t, err)
	transition := &blockingApprovalTransition{
		material: ApprovalGrantMaterial{PrincipalID: principal.Principal.ID, ServerID: id(51)},
		entered:  make(chan struct{}), release: make(chan struct{}),
	}
	first := make(chan error, 1)
	go func() {
		_, approveErr := repository.ApproveGrantRequest(context.Background(), transition)
		first <- approveErr
	}()
	<-transition.entered
	_, err = repository.ApproveGrantRequest(context.Background(), &staticApprovalTransition{
		material: ApprovalGrantMaterial{PrincipalID: principal.Principal.ID, ServerID: id(51)},
	})
	assert.ErrorIs(t, err, ErrApprovalUnavailable)
	close(transition.release)
	require.NoError(t, <-first)
}

func TestS5ApprovalGrantCapacityRollsBackBeforeRequestTransition(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{
		DisplayName: "Approval owner", Visibility: contract.VisibilityRequestable,
	})
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for index := int64(1); index < mustLimit("grants"); index++ {
			grantID := fmt.Sprintf("01J6%022d", index)
			if _, insertErr := transaction.Exec(`INSERT INTO grants
				(id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at)
				VALUES (?, ?, 'allow', ?, NULL, NULL, NULL, ?)`, grantID, principal.Principal.ID, id(51), timestamp(testNow)); insertErr != nil {
				return insertErr
			}
		}
		return nil
	}))
	before, err := repository.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	transition := &staticApprovalTransition{material: ApprovalGrantMaterial{PrincipalID: principal.Principal.ID, ServerID: id(51)}}

	_, err = repository.ApproveGrantRequest(context.Background(), transition)
	assert.ErrorIs(t, err, ErrResourceLimit)
	assert.Zero(t, transition.commitCalls)
	after, err := repository.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

type staticApprovalTransition struct {
	material    ApprovalGrantMaterial
	commitCalls int
}

func (transition *staticApprovalTransition) PrepareGrantRequestApproval(context.Context, *sql.Tx) (ApprovalGrantMaterial, error) {
	return transition.material, nil
}

func (transition *staticApprovalTransition) CommitGrantRequestApproval(_ context.Context, _ *sql.Tx, grantID string, _ time.Time) (contract.AgentGrantRequest, error) {
	transition.commitCalls++
	return approvedTransitionResult(grantID), nil
}

type blockingApprovalTransition struct {
	material ApprovalGrantMaterial
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (transition *blockingApprovalTransition) PrepareGrantRequestApproval(ctx context.Context, _ *sql.Tx) (ApprovalGrantMaterial, error) {
	transition.once.Do(func() { close(transition.entered) })
	select {
	case <-transition.release:
		return transition.material, nil
	case <-ctx.Done():
		return ApprovalGrantMaterial{}, ctx.Err()
	}
}

func (transition *blockingApprovalTransition) CommitGrantRequestApproval(_ context.Context, _ *sql.Tx, grantID string, _ time.Time) (contract.AgentGrantRequest, error) {
	return approvedTransitionResult(grantID), nil
}

func approvedTransitionResult(grantID string) contract.AgentGrantRequest {
	return contract.AgentGrantRequest{
		ID: id(70), State: contract.RequestApproved, Revision: "2", ApprovedGrantID: &grantID,
	}
}
