package grantrequests

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartupAcceptsCompleteLifecycleAndHistoricalDeletedGrantID(t *testing.T) {
	clock := &countingRequestClock{now: requestTestTime}
	descriptor := requestDescriptor(t, requestID(400), requestID(401), "sample", "echo", contract.EvidenceRetired)
	repository, store := newRequestRepository(t, requestRepositoryOptions{
		clock: clock,
		namespaces: &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}},
		descriptors: &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{"sample.echo": descriptor}},
	})
	principalID := requestID(200)
	policies := []contract.Policy{
		{Scope: contract.PolicyTool, Target: "sample.echo"},
		{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true},
		{Scope: contract.PolicyServer, Target: "sample", DurationSeconds: stringPointer("60"), FutureToolsAcknowledged: true},
		{Scope: contract.PolicyServer, Target: "sample", DurationSeconds: stringPointer("61"), FutureToolsAcknowledged: true},
	}
	created := make([]contract.AgentGrantRequest, 0, len(policies))
	for _, policy := range policies {
		result, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: principalID, Policy: policy})
		require.NoError(t, err)
		created = append(created, *result.Request)
	}
	clock.now = requestTestTime.Add(time.Second)
	_, err := repository.Reject(context.Background(), RejectRequest{ID: created[2].ID, ExpectedRevision: "1", Reason: contract.RejectionExistingAccess})
	require.NoError(t, err)
	_, err = repository.CancelOwned(context.Background(), principalID, created[3].ID)
	require.NoError(t, err)

	closedAt := requestTimestamp(clock.now)
	_, approvedEvidence, err := BuildDescriptorEvidence(descriptor, "sample", clock.now)
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, updateErr := transaction.ExecContext(context.Background(), `UPDATE grant_requests SET
			state = 'approved', revision = 2,
			approved_scope = 'tool', approved_target = 'sample.echo', approved_future_tools_acknowledged = 0,
			approved_grant_id = ?, approved_evidence = ?, updated_at = ?, closed_at = ?
			WHERE id = ? AND state = 'pending' AND revision = 1`,
			requestID(700), approvedEvidence, closedAt, closedAt, created[1].ID)
		return updateErr
	}))
	principals := &fakeStoredPrincipalInspector{existing: map[string]bool{principalID: true}}
	targets := &fakeStoredTargetInspector{namespaces: map[string]string{requestID(400): "sample"}}
	require.NoError(t, repository.ValidateStartup(context.Background(), principals, targets))
	assert.Equal(t, []string{principalID}, principals.lookups)
	assert.Equal(t, []string{requestID(400)}, targets.lookups)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, insertErr := transaction.ExecContext(context.Background(), `INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`, requestID(999), requestTimestamp(requestTestTime))
		return insertErr
	}))
	require.NoError(t, repository.ValidateStartup(context.Background(), principals, targets), "evicted historical identities need no live request or grant")
}

func TestStartupAcceptsApprovedHistoricalGrantAfterLiveGrantDeletion(t *testing.T) {
	clock := &countingRequestClock{now: requestTestTime}
	repository, store := newRequestRepository(t, requestRepositoryOptions{
		clock: clock,
		namespaces: &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
			"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		}},
	})
	authorityEntropy := make([]byte, 256)
	for index := range authorityEntropy {
		authorityEntropy[index] = byte(index%251 + 1)
	}
	authority, err := authorization.New(store, clock, bytes.NewReader(authorityEntropy))
	require.NoError(t, err)
	principal, err := authority.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{
		DisplayName: "Historical owner", Visibility: contract.VisibilityRequestable,
	})
	require.NoError(t, err)
	grant, err := authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{
		PrincipalID: principal.Principal.ID, Effect: contract.GrantAllow, ServerID: requestID(400),
	}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	created, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: principal.Principal.ID, Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	clock.now = requestTestTime.Add(time.Second)
	closed := requestTimestamp(clock.now)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, updateErr := transaction.Exec(`UPDATE grant_requests SET
			state = 'approved', revision = 2, approved_scope = 'server', approved_target = 'sample',
			approved_future_tools_acknowledged = 1, approved_grant_id = ?, updated_at = ?, closed_at = ?
			WHERE id = ?`, grant.ID, closed, closed, created.Request.ID)
		return updateErr
	}))
	require.NoError(t, authority.DeleteGrant(context.Background(), grant.ID))
	targets := &fakeStoredTargetInspector{namespaces: map[string]string{requestID(400): "sample"}}
	require.NoError(t, repository.ValidateStartup(context.Background(), authority, targets))
}

func TestStartupRejectsMissingOwnerTargetIdentityDedupeAndEvidence(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *Repository, *sqlStoreFixture, *fakeStoredPrincipalInspector, *fakeStoredTargetInspector)
	}{
		{name: "missing owner", prepare: func(_ *testing.T, _ *Repository, _ *sqlStoreFixture, principals *fakeStoredPrincipalInspector, _ *fakeStoredTargetInspector) {
			principals.existing[requestID(200)] = false
		}},
		{name: "missing target", prepare: func(_ *testing.T, _ *Repository, _ *sqlStoreFixture, _ *fakeStoredPrincipalInspector, targets *fakeStoredTargetInspector) {
			delete(targets.namespaces, requestID(400))
		}},
		{name: "missing permanent identity", prepare: func(t *testing.T, _ *Repository, fixture *sqlStoreFixture, _ *fakeStoredPrincipalInspector, _ *fakeStoredTargetInspector) {
			require.NoError(t, fixture.mutate(func(transaction *sql.Tx) error {
				_, err := transaction.Exec(`DELETE FROM grant_request_identities WHERE id = ?`, requestID(10))
				return err
			}))
		}},
		{name: "wrong dedupe", prepare: func(t *testing.T, _ *Repository, fixture *sqlStoreFixture, _ *fakeStoredPrincipalInspector, _ *fakeStoredTargetInspector) {
			require.NoError(t, fixture.mutate(func(transaction *sql.Tx) error {
				return insertStartupPending(transaction, requestID(11), requestID(200), contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true}, []byte("wrong"), nil, requestID(400), nil)
			}))
		}},
		{name: "malformed evidence", prepare: func(t *testing.T, _ *Repository, fixture *sqlStoreFixture, _ *fakeStoredPrincipalInspector, _ *fakeStoredTargetInspector) {
			require.NoError(t, fixture.mutate(func(transaction *sql.Tx) error {
				upstream := "echo"
				compiled := mustCompilePolicy(t, contract.Policy{Scope: contract.PolicyTool, Target: "sample.echo"})
				identity, err := CanonicalDedupeIdentity(compiled, ResolvedTarget{ServerID: requestID(400), UpstreamName: &upstream})
				require.NoError(t, err)
				return insertStartupPending(transaction, requestID(12), requestID(200), compiled.Contract(), identity.Bytes, []byte("x"), requestID(400), &upstream)
			}))
		}},
		{name: "synthetic target", prepare: func(t *testing.T, _ *Repository, fixture *sqlStoreFixture, _ *fakeStoredPrincipalInspector, targets *fakeStoredTargetInspector) {
			targets.namespaces[contract.SyntheticServerID] = contract.SyntheticServerNamespace
			require.NoError(t, fixture.mutate(func(transaction *sql.Tx) error {
				compiled := mustCompilePolicy(t, contract.Policy{Scope: contract.PolicyServer, Target: contract.SyntheticServerNamespace, FutureToolsAcknowledged: true})
				identity, err := CanonicalDedupeIdentity(compiled, ResolvedTarget{ServerID: contract.SyntheticServerID})
				require.NoError(t, err)
				return insertStartupPending(transaction, requestID(13), requestID(200), compiled.Contract(), identity.Bytes, nil, contract.SyntheticServerID, nil)
			}))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, store := newRequestRepository(t, requestRepositoryOptions{})
			fixture := &sqlStoreFixture{store: store}
			principals := &fakeStoredPrincipalInspector{existing: map[string]bool{requestID(200): true}}
			targets := &fakeStoredTargetInspector{namespaces: map[string]string{requestID(400): "sample"}}
			require.NoError(t, fixture.mutate(func(transaction *sql.Tx) error {
				compiled := mustCompilePolicy(t, contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true})
				identity, err := CanonicalDedupeIdentity(compiled, ResolvedTarget{ServerID: requestID(400)})
				require.NoError(t, err)
				return insertStartupPending(transaction, requestID(10), requestID(200), compiled.Contract(), identity.Bytes, nil, requestID(400), nil)
			}))
			test.prepare(t, repository, fixture, principals, targets)
			assert.ErrorIs(t, repository.ValidateStartup(context.Background(), principals, targets), ErrInvalidState)
		})
	}
}

func TestStartupRejectsPendingPerPrincipalCapacityOverflow(t *testing.T) {
	repository, store := newRequestRepository(t, requestRepositoryOptions{})
	principalID := requestID(200)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for index := int64(0); index <= fixedLimit("pending_grant_requests_per_principal"); index++ {
			duration := fmt.Sprintf("%d", 60+index)
			compiled := mustCompilePolicy(t, contract.Policy{Scope: contract.PolicyServer, Target: "sample", DurationSeconds: &duration, FutureToolsAcknowledged: true})
			identity, err := CanonicalDedupeIdentity(compiled, ResolvedTarget{ServerID: requestID(400)})
			if err != nil {
				return err
			}
			if err := insertStartupPending(transaction, requestID(int(index)+100), principalID, compiled.Contract(), identity.Bytes, nil, requestID(400), nil); err != nil {
				return err
			}
		}
		return nil
	}))
	principals := &fakeStoredPrincipalInspector{existing: map[string]bool{principalID: true}}
	targets := &fakeStoredTargetInspector{namespaces: map[string]string{requestID(400): "sample"}}
	assert.ErrorIs(t, repository.ValidateStartup(context.Background(), principals, targets), ErrInvalidState)
}

type fakeStoredPrincipalInspector struct {
	existing map[string]bool
	lookups  []string
}

func (inspector *fakeStoredPrincipalInspector) StoredPrincipalExistsTx(_ context.Context, transaction *sql.Tx, principalID string) (bool, error) {
	if transaction == nil {
		return false, fmt.Errorf("missing transaction")
	}
	inspector.lookups = append(inspector.lookups, principalID)
	return inspector.existing[principalID], nil
}

type fakeStoredTargetInspector struct {
	namespaces map[string]string
	lookups    []string
}

func (inspector *fakeStoredTargetInspector) LookupStoredGrantNamespaceTx(_ context.Context, transaction *sql.Tx, serverID string) (string, bool, error) {
	if transaction == nil {
		return "", false, fmt.Errorf("missing transaction")
	}
	inspector.lookups = append(inspector.lookups, serverID)
	namespace, found := inspector.namespaces[serverID]
	return namespace, found, nil
}

type sqlStoreFixture struct {
	store interface {
		Mutate(context.Context, func(*sql.Tx) error) error
	}
}

func (fixture *sqlStoreFixture) mutate(callback func(*sql.Tx) error) error {
	return fixture.store.Mutate(context.Background(), callback)
}

func insertStartupPending(transaction *sql.Tx, id, principalID string, policy contract.Policy, dedupe, evidence []byte, serverID string, upstream *string) error {
	created := requestTimestamp(requestTestTime)
	if _, err := transaction.Exec(`INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`, id, created); err != nil {
		return err
	}
	var constraint, duration any
	if policy.Constraint != nil {
		constraint = string(*policy.Constraint)
	}
	if policy.DurationSeconds != nil {
		duration = *policy.DurationSeconds
	}
	_, err := transaction.Exec(`INSERT INTO grant_requests (
		id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
		requested_scope, requested_target, requested_constraint, requested_duration_seconds,
		requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
		approved_scope, approved_target, approved_constraint, approved_duration_seconds,
		approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
		created_at, updated_at, closed_at
	) VALUES (?, ?, 'pending', 1, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?,
		NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?, NULL)`,
		id, principalID, serverID, upstream, policy.Scope, policy.Target, constraint, duration,
		policy.FutureToolsAcknowledged, dedupe, nullableEvidence(evidence), created, created)
	return err
}
