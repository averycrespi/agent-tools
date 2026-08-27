package grantrequests

import (
	"bytes"
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

func TestS5ApprovalSeamAtomicallyCreatesAllowAndApprovedRequest(t *testing.T) {
	fixture := newApprovalFixture(t)
	created := fixture.createRequest(t, contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	})
	fixture.descriptors.descriptors["sample.tool"] = requestDescriptor(
		t, requestID(400), requestID(500), "sample", "tool", contract.EvidenceRetired,
	)
	fixture.clock.now = requestTestTime.Add(time.Minute)
	beforeRevision, err := fixture.authority.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	fixture.invalidations = nil
	duration := "60"

	result, err := fixture.requests.Approve(context.Background(), fixture.authority, ApproveRequest{
		ID: created.ID, ExpectedRevision: "1", ApprovedPolicy: contract.Policy{
			Scope: contract.PolicyTool, Target: "sample.tool", DurationSeconds: &duration,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, contract.GrantAllow, result.Grant.Effect)
	assert.Equal(t, fixture.principal.Principal.ID, result.Grant.PrincipalID)
	assert.Equal(t, requestID(400), result.Grant.ServerID)
	require.NotNil(t, result.Grant.UpstreamName)
	assert.Equal(t, "tool", *result.Grant.UpstreamName)
	require.NotNil(t, result.Grant.ExpiresAt)
	assert.Equal(t, requestTimestamp(fixture.clock.now.Add(time.Minute)), *result.Grant.ExpiresAt)
	assert.Equal(t, contract.RequestApproved, result.Request.State)
	assert.Equal(t, "2", result.Request.Revision)
	require.NotNil(t, result.Request.ApprovedPolicy)
	assert.Equal(t, "sample.tool", result.Request.ApprovedPolicy.Target)
	require.NotNil(t, result.Request.ApprovedGrantID)
	assert.Equal(t, result.Grant.ID, *result.Request.ApprovedGrantID)
	assert.Equal(t, requestTimestamp(fixture.clock.now), *result.Request.ClosedAt)
	assert.Equal(t, []contract.Invalidation{
		{Kind: contract.InvalidationGrantRequests}, {Kind: contract.InvalidationAuthorization},
	}, fixture.invalidations)
	afterRevision, err := fixture.authority.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, revisionPlusOne(t, beforeRevision), afterRevision)

	var grantCount, approvedEvidenceBytes int64
	require.NoError(t, fixture.store.View(context.Background(), func(transaction *sql.Tx) error {
		if err := transaction.QueryRow(`SELECT count(*) FROM grants WHERE id = ?`, result.Grant.ID).Scan(&grantCount); err != nil {
			return err
		}
		return transaction.QueryRow(`SELECT length(approved_evidence) FROM grant_requests WHERE id = ?`, created.ID).Scan(&approvedEvidenceBytes)
	}))
	assert.Equal(t, int64(1), grantCount)
	assert.Positive(t, approvedEvidenceBytes)
	targets := &fakeStoredTargetInspector{namespaces: map[string]string{requestID(400): "sample"}}
	require.NoError(t, fixture.requests.ValidateStartup(context.Background(), fixture.authority, targets))
	require.NoError(t, fixture.authority.DeleteGrant(context.Background(), result.Grant.ID))
	historical, found, err := fixture.requests.GetOwned(context.Background(), fixture.principal.Principal.ID, created.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, contract.RequestApproved, historical.State)
	assert.Equal(t, result.Request.ApprovedGrantID, historical.ApprovedGrantID)
	require.NoError(t, fixture.requests.ValidateStartup(context.Background(), fixture.authority, targets))
}

func TestS5ApprovalEnforcesConstraintAndDurationNarrowing(t *testing.T) {
	baseConstraint := constraint(`{"equals":{"/x":1,"/y":"a"}}`)
	moreConstraint := constraint(`{"equals":{"/z":true,"/y":"a","/x":1}}`)
	droppedConstraint := constraint(`{"equals":{"/x":1}}`)
	lexicalConstraint := constraint(`{"equals":{"/x":1.0,"/y":"a"}}`)
	shorter, longer := "60", "61"
	tests := []struct {
		name      string
		requested contract.Policy
		approved  contract.Policy
		valid     bool
	}{
		{name: "unconstrained adds constraint", requested: policy(contract.PolicyTool, "sample.tool", nil, nil, false), approved: policy(contract.PolicyTool, "sample.tool", baseConstraint, nil, false), valid: true},
		{name: "retains and adds atoms", requested: policy(contract.PolicyTool, "sample.tool", baseConstraint, nil, false), approved: policy(contract.PolicyTool, "sample.tool", moreConstraint, nil, false), valid: true},
		{name: "drops atom", requested: policy(contract.PolicyTool, "sample.tool", baseConstraint, nil, false), approved: policy(contract.PolicyTool, "sample.tool", droppedConstraint, nil, false)},
		{name: "changes lexical number", requested: policy(contract.PolicyTool, "sample.tool", baseConstraint, nil, false), approved: policy(contract.PolicyTool, "sample.tool", lexicalConstraint, nil, false)},
		{name: "shortens duration", requested: policy(contract.PolicyTool, "sample.tool", nil, &longer, false), approved: policy(contract.PolicyTool, "sample.tool", nil, &shorter, false), valid: true},
		{name: "lengthens duration", requested: policy(contract.PolicyTool, "sample.tool", nil, &shorter, false), approved: policy(contract.PolicyTool, "sample.tool", nil, &longer, false)},
		{name: "temporary becomes permanent", requested: policy(contract.PolicyTool, "sample.tool", nil, &shorter, false), approved: policy(contract.PolicyTool, "sample.tool", nil, nil, false)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalFixture(t)
			fixture.descriptors.descriptors["sample.tool"] = requestDescriptor(t, requestID(400), requestID(500), "sample", "tool", contract.EvidenceCurrent)
			created := fixture.createRequest(t, test.requested)
			fixture.clock.now = requestTestTime.Add(time.Minute)
			fixture.descriptors.calls = 0
			result, err := fixture.requests.Approve(context.Background(), fixture.authority, ApproveRequest{
				ID: created.ID, ExpectedRevision: "1", ApprovedPolicy: test.approved,
			})
			if test.valid {
				require.NoError(t, err)
				assert.Equal(t, contract.RequestApproved, result.Request.State)
				assert.Zero(t, fixture.descriptors.calls, "exact requests rely on immutable submitted evidence")
			} else {
				assert.ErrorIs(t, err, ErrInvalidInput)
				stored, found, readErr := fixture.requests.GetOwned(context.Background(), fixture.principal.Principal.ID, created.ID)
				require.NoError(t, readErr)
				require.True(t, found)
				assert.Equal(t, contract.RequestPending, stored.State)
			}
		})
	}
}

func TestS5ApprovalKnownFailuresLeavePendingWithoutGrantOrRevision(t *testing.T) {
	tests := []struct {
		name          string
		arrange       func(*testing.T, *approvalFixture, contract.AgentGrantRequest)
		revision      string
		policy        contract.Policy
		expected      error
		expectedState contract.GrantRequestState
	}{
		{name: "stale revision", revision: "2", policy: serverApprovalPolicy(), expected: ErrStaleRevision},
		{name: "terminal current revision", revision: "2", policy: serverApprovalPolicy(), expected: ErrConflict, expectedState: contract.RequestCancelled,
			arrange: func(t *testing.T, fixture *approvalFixture, created contract.AgentGrantRequest) {
				_, err := fixture.requests.CancelOwned(context.Background(), fixture.principal.Principal.ID, created.ID)
				require.NoError(t, err)
			}},
		{name: "different server is not narrowing", revision: "1", expected: ErrInvalidInput,
			policy: contract.Policy{Scope: contract.PolicyServer, Target: "other", FutureToolsAcknowledged: true}},
		{name: "deleted target", revision: "1", policy: serverApprovalPolicy(), expected: ErrConflict,
			arrange: func(_ *testing.T, fixture *approvalFixture, _ contract.AgentGrantRequest) {
				fixture.namespaces.targets["sample"] = servers.NamespaceTarget{ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDeleted}
			}},
		{name: "unavailable approved evidence", revision: "1", expected: ErrConflict,
			policy: contract.Policy{Scope: contract.PolicyTool, Target: "sample.tool"}},
		{name: "deny conflict", revision: "1", policy: serverApprovalPolicy(), expected: ErrConflict,
			arrange: func(t *testing.T, fixture *approvalFixture, _ contract.AgentGrantRequest) {
				_, err := fixture.authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{
					PrincipalID: fixture.principal.Principal.ID, Effect: contract.GrantDeny, ServerID: requestID(400),
				}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
				require.NoError(t, err)
			}},
		{name: "evidence capacity", revision: "1", expected: ErrResourceLimit,
			policy: contract.Policy{Scope: contract.PolicyTool, Target: "sample.tool"},
			arrange: func(t *testing.T, fixture *approvalFixture, _ contract.AgentGrantRequest) {
				fixture.descriptors.descriptors["sample.tool"] = requestDescriptor(t, requestID(400), requestID(500), "sample", "tool", contract.EvidenceCurrent)
				require.NoError(t, fixture.store.Mutate(context.Background(), func(transaction *sql.Tx) error {
					_, err := transaction.Exec(`UPDATE grant_request_evidence_bytes SET total_bytes = ? WHERE singleton = 1`, fixedLimit("grant_request_evidence_bytes"))
					return err
				}))
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalFixture(t)
			created := fixture.createRequest(t, serverApprovalPolicy())
			if test.arrange != nil {
				test.arrange(t, fixture, created)
			}
			fixture.clock.now = requestTestTime.Add(time.Minute)
			beforeRevision, err := fixture.authority.AuthorizationRevision(context.Background())
			require.NoError(t, err)
			beforeGrants := approvalGrantCount(t, fixture.store)
			fixture.invalidations = nil

			_, err = fixture.requests.Approve(context.Background(), fixture.authority, ApproveRequest{
				ID: created.ID, ExpectedRevision: test.revision, ApprovedPolicy: test.policy,
			})
			assert.ErrorIs(t, err, test.expected)
			stored, found, readErr := fixture.requests.GetOwned(context.Background(), fixture.principal.Principal.ID, created.ID)
			require.NoError(t, readErr)
			require.True(t, found)
			expectedState := test.expectedState
			if expectedState == "" {
				expectedState = contract.RequestPending
			}
			assert.Equal(t, expectedState, stored.State)
			assert.Equal(t, beforeGrants, approvalGrantCount(t, fixture.store))
			afterRevision, revisionErr := fixture.authority.AuthorizationRevision(context.Background())
			require.NoError(t, revisionErr)
			assert.Equal(t, beforeRevision, afterRevision)
			assert.Empty(t, fixture.invalidations)
		})
	}
}

func TestS5ApprovalConditionalBarriersHaveOneWinner(t *testing.T) {
	for _, loser := range []string{"cancel", "reject", "deny"} {
		t.Run(loser, func(t *testing.T) {
			fixture := newApprovalFixture(t)
			created := fixture.createRequest(t, serverApprovalPolicy())
			fixture.clock.now = requestTestTime.Add(time.Minute)
			entered, release := make(chan struct{}), make(chan struct{})
			fixture.requests.namespaces = &blockingNamespaceInspector{
				delegate: fixture.namespaces, entered: entered, release: release,
			}
			approval := make(chan error, 1)
			go func() {
				_, err := fixture.requests.Approve(context.Background(), fixture.authority, ApproveRequest{
					ID: created.ID, ExpectedRevision: "1", ApprovedPolicy: serverApprovalPolicy(),
				})
				approval <- err
			}()
			<-entered
			switch loser {
			case "cancel":
				_, err := fixture.requests.CancelOwned(context.Background(), fixture.principal.Principal.ID, created.ID)
				assert.ErrorIs(t, err, ErrStorageUnavailable)
			case "reject":
				_, err := fixture.requests.Reject(context.Background(), RejectRequest{
					ID: created.ID, ExpectedRevision: "1", Reason: contract.RejectionNotApproved,
				})
				assert.ErrorIs(t, err, ErrStorageUnavailable)
			case "deny":
				_, err := fixture.authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{
					PrincipalID: fixture.principal.Principal.ID, Effect: contract.GrantDeny, ServerID: requestID(400),
				}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
				assert.ErrorIs(t, err, authorization.ErrResourceLimit)
			}
			close(release)
			require.NoError(t, <-approval)
			stored, found, err := fixture.requests.GetOwned(context.Background(), fixture.principal.Principal.ID, created.ID)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, contract.RequestApproved, stored.State)
		})
	}
}

type approvalFixture struct {
	requests      *Repository
	authority     *authorization.Repository
	store         *storage.Store
	clock         *countingRequestClock
	namespaces    *fakeNamespaceInspector
	descriptors   *fakeDescriptorInspector
	principal     contract.PrincipalCreation
	invalidations []contract.Invalidation
}

func newApprovalFixture(t *testing.T) *approvalFixture {
	t.Helper()
	clock := &countingRequestClock{now: requestTestTime}
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		"other":  {ID: requestID(401), Namespace: "other", State: contract.DesiredServerDisabled},
	}}
	descriptors := &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{}}
	fixture := &approvalFixture{clock: clock, namespaces: namespaces, descriptors: descriptors}
	fixture.requests, fixture.store = newRequestRepository(t, requestRepositoryOptions{
		clock: clock, namespaces: namespaces, descriptors: descriptors,
		invalidate: func(event contract.Invalidation) { fixture.invalidations = append(fixture.invalidations, event) },
	})
	entropy := make([]byte, 512)
	for index := range entropy {
		entropy[index] = byte(index%251 + 1)
	}
	var err error
	fixture.authority, err = authorization.New(fixture.store, clock, bytes.NewReader(entropy))
	require.NoError(t, err)
	fixture.principal, err = fixture.authority.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{
		DisplayName: "Request owner", Visibility: contract.VisibilityRequestable,
	})
	require.NoError(t, err)
	return fixture
}

func (fixture *approvalFixture) createRequest(t *testing.T, policy contract.Policy) contract.AgentGrantRequest {
	t.Helper()
	result, err := fixture.requests.CreateOrExisting(context.Background(), CreateRequest{
		PrincipalID: fixture.principal.Principal.ID, Policy: policy,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Request)
	return *result.Request
}

func serverApprovalPolicy() contract.Policy {
	return contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true}
}

func approvalGrantCount(t *testing.T, store *storage.Store) int64 {
	t.Helper()
	var count int64
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT count(*) FROM grants`).Scan(&count)
	}))
	return count
}

func revisionPlusOne(t *testing.T, value string) string {
	t.Helper()
	revision, err := strconv.ParseInt(value, 10, 64)
	require.NoError(t, err)
	return strconv.FormatInt(revision+1, 10)
}
