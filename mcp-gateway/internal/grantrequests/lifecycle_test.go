package grantrequests

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS5RequestLifecycleSeamCancellationIsOwnerOnlyAndIdempotent(t *testing.T) {
	clock := &countingRequestClock{now: requestTestTime.Add(time.Second)}
	invalidations := 0
	repository, _ := newRequestRepository(t, requestRepositoryOptions{
		clock: clock,
		namespaces: &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}},
		invalidate: func(contract.Invalidation) { invalidations++ },
	})
	created, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	clock.calls = 0
	invalidations = 0

	foreign, err := repository.CancelOwned(context.Background(), requestID(201), created.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.RequestCancellationNotFound, foreign.Outcome)
	assert.Nil(t, foreign.Request)
	assert.Zero(t, clock.calls)
	assert.Zero(t, invalidations)

	cancelled, err := repository.CancelOwned(context.Background(), requestID(200), created.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.RequestCancellationCancelled, cancelled.Outcome)
	require.NotNil(t, cancelled.Request)
	assert.Equal(t, contract.RequestCancelled, cancelled.Request.State)
	assert.Equal(t, "2", cancelled.Request.Revision)
	closed := requestTimestamp(clock.now)
	assert.Equal(t, &closed, cancelled.Request.ClosedAt)
	assert.Equal(t, closed, cancelled.Request.UpdatedAt)
	assert.Equal(t, 1, clock.calls)
	assert.Equal(t, 1, invalidations)

	repeated, err := repository.CancelOwned(context.Background(), requestID(200), created.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.RequestCancellationAlreadyCancelled, repeated.Outcome)
	assert.Equal(t, cancelled.Request, repeated.Request)
	assert.Equal(t, 1, clock.calls)
	assert.Equal(t, 1, invalidations)
}

func TestS5RejectValidatesRevisionReasonAndTerminalState(t *testing.T) {
	for _, reason := range contract.GrantRequestRejectionReasons() {
		t.Run(string(reason), func(t *testing.T) {
			clock := &countingRequestClock{now: requestTestTime.Add(time.Second)}
			invalidations := 0
			repository, _ := newRequestRepository(t, requestRepositoryOptions{
				clock: clock,
				namespaces: &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
					"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
				}},
				invalidate: func(contract.Invalidation) { invalidations++ },
			})
			created, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
				Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
			}})
			require.NoError(t, err)
			clock.calls = 0
			invalidations = 0

			_, err = repository.Reject(context.Background(), RejectRequest{ID: created.Request.ID, ExpectedRevision: "2", Reason: reason})
			assert.ErrorIs(t, err, ErrStaleRevision)
			assert.Zero(t, clock.calls)
			rejected, err := repository.Reject(context.Background(), RejectRequest{ID: created.Request.ID, ExpectedRevision: "1", Reason: reason})
			require.NoError(t, err)
			assert.Equal(t, contract.RequestRejected, rejected.State)
			assert.Equal(t, "2", rejected.Revision)
			require.NotNil(t, rejected.RejectionReason)
			assert.Equal(t, reason, *rejected.RejectionReason)
			assert.Equal(t, 1, invalidations)

			_, err = repository.Reject(context.Background(), RejectRequest{ID: created.Request.ID, ExpectedRevision: "1", Reason: reason})
			assert.ErrorIs(t, err, ErrStaleRevision)
			_, err = repository.Reject(context.Background(), RejectRequest{ID: created.Request.ID, ExpectedRevision: "2", Reason: reason})
			assert.ErrorIs(t, err, ErrConflict)
		})
	}

	repository, _ := newRequestRepository(t, requestRepositoryOptions{})
	_, err := repository.Reject(context.Background(), RejectRequest{ID: requestID(999), ExpectedRevision: "1", Reason: contract.RejectionNotApproved})
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = repository.Reject(context.Background(), RejectRequest{ID: requestID(999), ExpectedRevision: "01", Reason: contract.RejectionNotApproved})
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, err = repository.Reject(context.Background(), RejectRequest{ID: requestID(999), ExpectedRevision: "1", Reason: "unknown"})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestS5RequestLifecycleClockFailureRollsBackWithoutInvalidation(t *testing.T) {
	clock := &countingRequestClock{now: requestTestTime}
	invalidations := 0
	repository, _ := newRequestRepository(t, requestRepositoryOptions{
		clock: clock,
		namespaces: &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}},
		invalidate: func(contract.Invalidation) { invalidations++ },
	})
	created, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	invalidations = 0
	clock.now = requestTestTime.Add(-time.Nanosecond)
	_, err = repository.CancelOwned(context.Background(), requestID(200), created.Request.ID)
	assert.ErrorIs(t, err, ErrIdentityUnavailable)
	assert.Zero(t, invalidations)
	stored, found, err := repository.GetOwned(context.Background(), requestID(200), created.Request.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, contract.RequestPending, stored.State)
	assert.Equal(t, "1", stored.Revision)
}

func TestS5CancelAndRejectConditionalBarrierHasOneTerminalWinner(t *testing.T) {
	clock := &blockingLifecycleClock{now: requestTestTime.Add(time.Second), entered: make(chan struct{}), release: make(chan struct{})}
	repository, _ := newRequestRepository(t, requestRepositoryOptions{
		clock: clock,
		namespaces: &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}},
		invalidate: func(contract.Invalidation) {},
	})
	created, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	clock.block = true
	type cancellation struct {
		result contract.CancelGrantRequestResult
		err    error
	}
	cancelled := make(chan cancellation, 1)
	go func() {
		result, cancelErr := repository.CancelOwned(context.Background(), requestID(200), created.Request.ID)
		cancelled <- cancellation{result: result, err: cancelErr}
	}()
	<-clock.entered
	_, err = repository.Reject(context.Background(), RejectRequest{
		ID: created.Request.ID, ExpectedRevision: "1", Reason: contract.RejectionPolicyConflict,
	})
	require.ErrorIs(t, err, ErrStorageUnavailable)
	close(clock.release)
	winner := <-cancelled
	require.NoError(t, winner.err)
	assert.Equal(t, contract.RequestCancellationCancelled, winner.result.Outcome)
	_, err = repository.Reject(context.Background(), RejectRequest{
		ID: created.Request.ID, ExpectedRevision: "1", Reason: contract.RejectionPolicyConflict,
	})
	assert.ErrorIs(t, err, ErrStaleRevision)
}

func TestS5CancelDoesNotAlterApprovedOrRejectedRequests(t *testing.T) {
	repository, store := newRequestRepository(t, requestRepositoryOptions{})
	for index, state := range []contract.GrantRequestState{contract.RequestApproved, contract.RequestRejected} {
		id := requestID(800 + index)
		seedTerminalRequest(t, store, id, requestID(200), state)
		result, err := repository.CancelOwned(context.Background(), requestID(200), id)
		require.NoError(t, err)
		assert.Equal(t, contract.RequestCancellationNotPending, result.Outcome)
		require.NotNil(t, result.Request)
		assert.Equal(t, state, result.Request.State)
	}
}

type blockingLifecycleClock struct {
	now     time.Time
	block   bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (clock *blockingLifecycleClock) Now() time.Time {
	if clock.block {
		clock.once.Do(func() { close(clock.entered) })
		<-clock.release
	}
	return clock.now
}

func seedTerminalRequest(t *testing.T, store interface {
	Mutate(context.Context, func(*sql.Tx) error) error
}, id, principalID string, state contract.GrantRequestState) {
	t.Helper()
	created := requestTimestamp(requestTestTime)
	closed := requestTimestamp(requestTestTime.Add(time.Second))
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		if _, err := transaction.Exec(`INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`, id, created); err != nil {
			return err
		}
		if state == contract.RequestApproved {
			_, err := transaction.Exec(`INSERT INTO grant_requests (
				id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
				requested_scope, requested_target, requested_constraint, requested_duration_seconds,
				requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
				approved_scope, approved_target, approved_constraint, approved_duration_seconds,
				approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
				created_at, updated_at, closed_at
			) VALUES (?, ?, 'approved', 2, ?, NULL, 'server', 'sample', NULL, NULL, 1, 1, ?, NULL,
				'server', 'sample', NULL, NULL, 1, ?, NULL, NULL, ?, ?, ?)`,
				id, principalID, requestID(400), []byte("terminal-"+id), requestID(700), created, closed, closed)
			return err
		}
		_, err := transaction.Exec(`INSERT INTO grant_requests (
			id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
			requested_scope, requested_target, requested_constraint, requested_duration_seconds,
			requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
			approved_scope, approved_target, approved_constraint, approved_duration_seconds,
			approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
			created_at, updated_at, closed_at
		) VALUES (?, ?, 'rejected', 2, ?, NULL, 'server', 'sample', NULL, NULL, 1, 1, ?, NULL,
			NULL, NULL, NULL, NULL, NULL, NULL, 'not_approved', NULL, ?, ?, ?)`,
			id, principalID, requestID(400), []byte("terminal-"+id), created, closed, closed)
		return err
	}))
}
