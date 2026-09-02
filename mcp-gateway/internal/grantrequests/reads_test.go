package grantrequests

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestReadOwnerOnlyProjectionAndPinnedPagination(t *testing.T) {
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	descriptors := &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{
		"sample.echo": requestDescriptor(t, requestID(400), requestID(401), "sample", "echo", contract.EvidenceCurrent),
	}}
	repository, store := newRequestRepository(t, requestRepositoryOptions{
		clock: &countingRequestClock{now: requestTestTime}, namespaces: namespaces,
		descriptors: descriptors, denies: new(fakeDenyInspector), invalidate: func(contract.Invalidation) {},
	})
	principalID := requestID(200)
	created := make([]contract.AgentGrantRequest, 0, 5)
	for _, policy := range []contract.Policy{
		{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true},
		{Scope: contract.PolicyServer, Target: "sample", DurationSeconds: stringPointer("60"), FutureToolsAcknowledged: true},
		{Scope: contract.PolicyServer, Target: "sample", DurationSeconds: stringPointer("61"), FutureToolsAcknowledged: true},
		{Scope: contract.PolicyServer, Target: "sample", DurationSeconds: stringPointer("62"), FutureToolsAcknowledged: true},
		{Scope: contract.PolicyTool, Target: "sample.echo"},
	} {
		result, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: principalID, Policy: policy})
		require.NoError(t, err)
		require.Equal(t, contract.RequestCreated, result.Outcome)
		created = append(created, *result.Request)
	}
	foreign, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(201), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)

	closedAt := requestTimestamp(requestTestTime.Add(time.Second))
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		if _, err := transaction.ExecContext(context.Background(), `UPDATE grant_requests SET
			state = 'approved', revision = 2, approved_scope = 'server', approved_target = 'sample',
			approved_duration_seconds = '60', approved_future_tools_acknowledged = 1,
			approved_grant_id = ?, updated_at = ?, closed_at = ? WHERE id = ?`,
			requestID(700), closedAt, closedAt, created[1].ID); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(context.Background(), `UPDATE grant_requests SET
			state = 'rejected', revision = 2, rejection_reason = 'scope_too_broad', updated_at = ?, closed_at = ? WHERE id = ?`,
			closedAt, closedAt, created[2].ID); err != nil {
			return err
		}
		_, err := transaction.ExecContext(context.Background(), `UPDATE grant_requests SET
			state = 'cancelled', revision = 2, updated_at = ?, closed_at = ? WHERE id = ?`,
			closedAt, closedAt, created[3].ID)
		return err
	}))

	approved, found, err := repository.GetOwned(context.Background(), principalID, created[1].ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, contract.RequestApproved, approved.State)
	assert.Equal(t, "2", approved.Revision)
	require.NotNil(t, approved.ApprovedPolicy)
	assert.Equal(t, "sample", approved.ApprovedPolicy.Target)
	require.NotNil(t, approved.ApprovedGrantID)
	assert.Equal(t, requestID(700), *approved.ApprovedGrantID)
	assert.Equal(t, &closedAt, approved.ClosedAt)

	_, found, err = repository.GetOwned(context.Background(), principalID, foreign.Request.ID)
	require.NoError(t, err)
	assert.False(t, found)
	_, found, err = repository.GetOwned(context.Background(), principalID, requestID(999))
	require.NoError(t, err)
	assert.False(t, found)

	first, err := repository.ListOwned(context.Background(), principalID, nil, nil, 2)
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotNil(t, first.Next)
	later, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: principalID, Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", DurationSeconds: stringPointer("63"), FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	second, err := repository.ListOwned(context.Background(), principalID, nil, first.Next, 100)
	require.NoError(t, err)
	require.Len(t, second.Items, 3)
	assert.Nil(t, second.Next)
	for _, item := range second.Items {
		assert.NotEqual(t, later.Request.ID, item.ID)
	}

	approvedState := contract.RequestApproved
	approvedPage, err := repository.ListOwned(context.Background(), principalID, &approvedState, nil, 100)
	require.NoError(t, err)
	require.Len(t, approvedPage.Items, 1)
	assert.Equal(t, created[1].ID, approvedPage.Items[0].ID)

	projected, err := json.Marshal(append(first.Items, second.Items...))
	require.NoError(t, err)
	assert.NotContains(t, string(projected), "resolved_server_id")
	assert.NotContains(t, string(projected), requestID(400))
	assert.NotContains(t, string(projected), "submitted_evidence")
	assert.NotContains(t, string(projected), "fingerprint")
}

func TestRequestReadSelectsOnlyAgentProjectionColumns(t *testing.T) {
	for _, forbidden := range []string{
		"principal_id", "resolved_server_id", "resolved_upstream_name", "dedupe_version", "dedupe_bytes",
		"submitted_evidence", "approved_evidence",
	} {
		assert.NotContains(t, agentRequestSelect, forbidden)
	}
}

func TestRequestReadRejectsMalformedInputsAndRowsWithoutPartialProjection(t *testing.T) {
	repository, store := newRequestRepository(t, requestRepositoryOptions{})
	_, _, err := repository.GetOwned(context.Background(), "malformed", requestID(1))
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, _, err = repository.GetOwned(context.Background(), requestID(1), "malformed")
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, err = repository.ListOwned(context.Background(), requestID(1), nil, nil, 0)
	assert.ErrorIs(t, err, ErrInvalidInput)
	invalidState := contract.GrantRequestState("unknown")
	_, err = repository.ListOwned(context.Background(), requestID(1), &invalidState, nil, 100)
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, err = repository.ListOwned(context.Background(), requestID(1), nil, &SelfCursor{Upper: 1, After: 2, AfterID: requestID(2)}, 100)
	assert.ErrorIs(t, err, ErrStaleCursor)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(context.Background(), `INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`, requestID(10), requestTimestamp(requestTestTime))
		if err != nil {
			return err
		}
		_, err = transaction.ExecContext(context.Background(), `INSERT INTO grant_requests (
			id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
			requested_scope, requested_target, requested_constraint, requested_duration_seconds,
			requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
			approved_scope, approved_target, approved_constraint, approved_duration_seconds,
			approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
			created_at, updated_at, closed_at
		) VALUES (?, ?, 'pending', 1, ?, NULL, 'server', 'INVALID', NULL, NULL, 1, 1, X'01', NULL,
			NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?, NULL)`,
			requestID(10), requestID(1), requestID(400), requestTimestamp(requestTestTime), requestTimestamp(requestTestTime))
		return err
	}))
	page, err := repository.ListOwned(context.Background(), requestID(1), nil, nil, 100)
	assert.ErrorIs(t, err, ErrInvalidState)
	assert.Empty(t, page.Items)
}
