package grantrequests

import (
	"context"
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS5StatusReadsGlobalRequestAndEvidenceOccupancyFromOwner(t *testing.T) {
	descriptor := requestDescriptor(t, requestID(400), requestID(500), "sample", "echo", contract.EvidenceCurrent)
	repository, store := newRequestRepository(t, requestRepositoryOptions{
		namespaces: &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}},
		descriptors: &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{"sample.echo": descriptor}},
	})
	requests, evidence, err := repository.Occupancy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, contract.LimitStatus{Limit: fixedLimit("grant_requests")}, requests)
	assert.Equal(t, contract.LimitStatus{Limit: fixedLimit("grant_request_evidence_bytes")}, evidence)

	_, err = repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{Scope: contract.PolicyTool, Target: "sample.echo"}})
	require.NoError(t, err)
	requests, evidence, err = repository.Occupancy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), requests.InUse)
	assert.Positive(t, evidence.InUse)
	assert.False(t, requests.Saturated)
	assert.False(t, evidence.Saturated)

	require.NoError(t, store.Close())
	_, _, err = repository.Occupancy(context.Background())
	assert.Error(t, err)
}

func TestS5StatusReportsNAndRejectsNPlusOne(t *testing.T) {
	repository, store := newRequestRepository(t, requestRepositoryOptions{})
	limit := fixedLimit("grant_requests")
	timestamp := requestTimestamp(requestTestTime)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		if _, err := transaction.Exec(`WITH RECURSIVE sequence(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM sequence WHERE n + 1 < ?)
			INSERT INTO grant_request_identities (id, created_at)
			SELECT printf('01J6000000000000000000%04d', n), ? FROM sequence`, limit, timestamp); err != nil {
			return err
		}
		_, err := transaction.Exec(`WITH RECURSIVE sequence(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM sequence WHERE n + 1 < ?)
			INSERT INTO grant_requests (
				id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
				requested_scope, requested_target, requested_constraint, requested_duration_seconds,
				requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
				approved_scope, approved_target, approved_constraint, approved_duration_seconds,
				approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
				created_at, updated_at, closed_at
			)
			SELECT printf('01J6000000000000000000%04d', n), ?, 'cancelled', 2, ?, NULL,
				'server', 'sample', NULL, NULL, 1, 1, CAST(printf('dedupe-%d', n) AS BLOB), NULL,
				NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?, ? FROM sequence`,
			limit, requestID(200), requestID(400), timestamp, timestamp, timestamp)
		return err
	}))
	requests, evidence, err := repository.Occupancy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, limit, requests.InUse)
	assert.True(t, requests.Saturated)
	assert.Zero(t, evidence.InUse)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		id := "01J60000000000000000004096"
		if _, err := transaction.Exec(`INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`, id, timestamp); err != nil {
			return err
		}
		_, err := transaction.Exec(`INSERT INTO grant_requests (
			id, principal_id, state, revision, resolved_server_id, requested_scope, requested_target,
			requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, created_at, updated_at, closed_at
		) VALUES (?, ?, 'cancelled', 2, ?, 'server', 'sample', 1, 1, X'01', ?, ?, ?)`,
			id, requestID(200), requestID(400), timestamp, timestamp, timestamp)
		return err
	}))
	_, _, err = repository.Occupancy(context.Background())
	assert.ErrorIs(t, err, ErrInvalidState)
}

func TestS5EventsRequestMutationsPublishOnlyIDFreeRequestInvalidations(t *testing.T) {
	invalidations := make([]contract.Invalidation, 0)
	repository, _ := newRequestRepository(t, requestRepositoryOptions{
		namespaces: &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}},
		invalidate: func(invalidation contract.Invalidation) { invalidations = append(invalidations, invalidation) },
	})
	policy := contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true}
	created, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: policy})
	require.NoError(t, err)
	assert.Equal(t, []contract.Invalidation{{Kind: contract.InvalidationGrantRequests}}, invalidations)
	invalidations = nil
	_, err = repository.CancelOwned(context.Background(), requestID(201), created.Request.ID)
	require.NoError(t, err)
	assert.Empty(t, invalidations)
	_, err = repository.CancelOwned(context.Background(), requestID(200), created.Request.ID)
	require.NoError(t, err)
	assert.Equal(t, []contract.Invalidation{{Kind: contract.InvalidationGrantRequests}}, invalidations)

	invalidations = nil
	second, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{Scope: contract.PolicyServer, Target: "sample", DurationSeconds: stringPointer("60"), FutureToolsAcknowledged: true}})
	require.NoError(t, err)
	invalidations = nil
	_, err = repository.Reject(context.Background(), RejectRequest{ID: second.Request.ID, ExpectedRevision: "2", Reason: contract.RejectionNotApproved})
	assert.ErrorIs(t, err, ErrStaleRevision)
	assert.Empty(t, invalidations)
	_, err = repository.Reject(context.Background(), RejectRequest{ID: second.Request.ID, ExpectedRevision: "1", Reason: contract.RejectionNotApproved})
	require.NoError(t, err)
	assert.Equal(t, []contract.Invalidation{{Kind: contract.InvalidationGrantRequests}}, invalidations)
	for _, invalidation := range invalidations {
		assert.Nil(t, invalidation.ResourceID)
	}
}

func TestS5EventsApprovalPublishesRequestThenAuthorizationOnlyOnSuccess(t *testing.T) {
	fixture := newApprovalFixture(t)
	created := fixture.createRequest(t, contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true})
	fixture.invalidations = nil
	policy := contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true}

	_, err := fixture.requests.Approve(context.Background(), fixture.authority, ApproveRequest{ID: created.ID, ExpectedRevision: "2", ApprovedPolicy: policy})
	assert.ErrorIs(t, err, ErrStaleRevision)
	assert.Empty(t, fixture.invalidations)

	_, err = fixture.requests.Approve(context.Background(), fixture.authority, ApproveRequest{ID: created.ID, ExpectedRevision: "1", ApprovedPolicy: policy})
	require.NoError(t, err)
	assert.Equal(t, []contract.Invalidation{
		{Kind: contract.InvalidationGrantRequests},
		{Kind: contract.InvalidationAuthorization},
	}, fixture.invalidations)
	for _, invalidation := range fixture.invalidations {
		assert.Nil(t, invalidation.ResourceID)
	}
}
