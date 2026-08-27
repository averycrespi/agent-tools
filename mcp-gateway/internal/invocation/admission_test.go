package invocation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditAdmissionRecordsBindingOnlyBranchesWithoutPolicyEvidence(t *testing.T) {
	for _, class := range []contract.InvocationAdmissionClass{
		contract.AdmissionInvalidParams,
		contract.AdmissionUnknownTool,
		contract.AdmissionInvalidArguments,
	} {
		t.Run(string(class), func(t *testing.T) {
			coordinator, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			lease, err := authority.Authenticate(context.Background(), credential.Bearer)
			require.NoError(t, err)
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			request := testAuditRequest(class)

			result, err := coordinator.Admit(context.Background(), lease, identity, request)

			require.NoError(t, err)
			assert.True(t, result.Committed)
			assert.False(t, result.DispatchAuthorized)
			assert.Equal(t, class, result.Class)
			record, found, readErr := audits.Read(context.Background(), identity.InvocationID)
			require.NoError(t, readErr)
			require.True(t, found)
			assert.Nil(t, record.AuthorizationDecision)
			assert.Nil(t, record.AuthorizationRevision)
			assert.Nil(t, record.EvaluatedAt)
			assert.Nil(t, record.GrantID)
			lease.Release()
		})
	}
}

func TestAuditAdmissionReevaluatesCurrentPolicyAndDetachesOnlyCommittedAllow(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		coordinator, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
		lease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		identity, err := audits.PrepareIdentity()
		require.NoError(t, err)

		result, err := coordinator.Admit(context.Background(), lease, identity, testAuditRequest(contract.AdmissionEvaluated))

		require.NoError(t, err)
		assert.True(t, result.Committed)
		assert.True(t, result.DispatchAuthorized)
		require.NotNil(t, result.Subject)
		assert.True(t, authority.OwnsAdmittedSubject(*result.Subject))
		require.NotNil(t, result.Decision)
		assert.Equal(t, contract.DecisionAllow, *result.Decision)
		authority.BeginDrain()
		select {
		case <-lease.Done():
			t.Fatal("committed ALLOW lease remained pending")
		default:
		}
		lease.Release()
	})

	t.Run("deny created after authentication", func(t *testing.T) {
		coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
		lease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		name := "tool"
		_, err = authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{
			PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &name,
		}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
		require.NoError(t, err)
		identity, err := audits.PrepareIdentity()
		require.NoError(t, err)

		result, err := coordinator.Admit(context.Background(), lease, identity, testAuditRequest(contract.AdmissionEvaluated))

		require.NoError(t, err)
		assert.True(t, result.Committed)
		assert.False(t, result.DispatchAuthorized)
		assert.Nil(t, result.Subject)
		require.NotNil(t, result.Decision)
		assert.Equal(t, contract.DecisionDeny, *result.Decision)
		record, found, readErr := audits.Read(context.Background(), identity.InvocationID)
		require.NoError(t, readErr)
		require.True(t, found)
		assert.Equal(t, contract.DecisionDeny, *record.AuthorizationDecision)
		lease.Release()
	})

	t.Run("block on unmatched target", func(t *testing.T) {
		coordinator, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
		lease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		identity, err := audits.PrepareIdentity()
		require.NoError(t, err)
		request := testAuditRequest(contract.AdmissionEvaluated)
		request.Route.ServerID = invocationID(500)

		result, err := coordinator.Admit(context.Background(), lease, identity, request)

		require.NoError(t, err)
		assert.False(t, result.DispatchAuthorized)
		assert.Nil(t, result.Subject)
		require.NotNil(t, result.Decision)
		assert.Equal(t, contract.DecisionBlock, *result.Decision)
		lease.Release()
	})
}

func TestAuditAdmissionCommitsUnavailableOnlyAfterVerifiedBinding(t *testing.T) {
	t.Run("evaluator semantic failure", func(t *testing.T) {
		coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
		lease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		insertMalformedEvaluationGrant(t, audits.store, principal.ID)
		identity, err := audits.PrepareIdentity()
		require.NoError(t, err)

		result, err := coordinator.Admit(context.Background(), lease, identity, testAuditRequest(contract.AdmissionEvaluated))

		require.NoError(t, err)
		assert.True(t, result.Committed)
		assert.Equal(t, contract.AdmissionAuthorizationUnavailable, result.Class)
		assert.Nil(t, result.Decision)
		record, found, readErr := audits.Read(context.Background(), identity.InvocationID)
		require.NoError(t, readErr)
		require.True(t, found)
		assert.Equal(t, contract.AdmissionAuthorizationUnavailable, record.AdmissionClass)
		assert.Nil(t, record.AuthorizationDecision)
		lease.Release()
	})

	t.Run("binding semantic failure", func(t *testing.T) {
		coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
		lease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		require.NoError(t, audits.store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			_, pragmaErr := transaction.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`)
			if pragmaErr != nil {
				return pragmaErr
			}
			_, updateErr := transaction.ExecContext(context.Background(), `UPDATE principals SET credential_fingerprint = 'bad' WHERE id = ?`, principal.ID)
			return updateErr
		}))
		identity, err := audits.PrepareIdentity()
		require.NoError(t, err)

		result, err := coordinator.Admit(context.Background(), lease, identity, testAuditRequest(contract.AdmissionEvaluated))

		assert.ErrorIs(t, err, authorization.ErrAuthorizationUnavailable)
		assert.False(t, result.Committed)
		count, countErr := audits.Count(context.Background())
		require.NoError(t, countErr)
		assert.Zero(t, count)
		lease.Release()
	})

	t.Run("evaluator storage failure", func(t *testing.T) {
		coordinator, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
		lease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		require.NoError(t, audits.store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			_, dropErr := transaction.ExecContext(context.Background(), `DROP TABLE grants`)
			return dropErr
		}))
		identity, err := audits.PrepareIdentity()
		require.NoError(t, err)

		result, err := coordinator.Admit(context.Background(), lease, identity, testAuditRequest(contract.AdmissionEvaluated))

		assert.ErrorIs(t, err, authorization.ErrStorageUnavailable)
		assert.False(t, result.Committed)
		count, countErr := audits.Count(context.Background())
		require.NoError(t, countErr)
		assert.Zero(t, count)
		lease.Release()
	})
}

func TestAuditAdmissionCommitUncertaintyAndLateDrainNeverDispatch(t *testing.T) {
	var authority *authorization.Repository
	armDrain, armFailure := false, false
	fault := func(point storage.FaultPoint) error {
		if point != storage.FaultAfterCommit {
			return nil
		}
		if armDrain {
			authority.BeginDrain()
		}
		if armFailure {
			return errors.New("commit acknowledgement lost")
		}
		return nil
	}
	coordinator, audits, builtAuthority, _, credential := newAdmissionCoordinator(t, fault)
	authority = builtAuthority

	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	identity, err := audits.PrepareIdentity()
	require.NoError(t, err)
	armDrain = true
	result, err := coordinator.Admit(context.Background(), lease, identity, testAuditRequest(contract.AdmissionEvaluated))
	assert.ErrorIs(t, err, authorization.ErrAdmissionUnavailable)
	assert.True(t, result.Committed)
	assert.False(t, result.DispatchAuthorized)

	// A separate repository proves an uncertain acknowledged commit never detaches.
	armDrain = false
	coordinator, audits, authority, _, credential = newAdmissionCoordinator(t, fault)
	lease, err = authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	identity, err = audits.PrepareIdentity()
	require.NoError(t, err)
	armFailure = true
	result, err = coordinator.Admit(context.Background(), lease, identity, testAuditRequest(contract.AdmissionEvaluated))
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.False(t, result.Committed)
	assert.False(t, result.DispatchAuthorized)
	assert.True(t, audits.store.Latched())
	lease.Release()
}

func newAdmissionCoordinator(t *testing.T, fault func(storage.FaultPoint) error) (*AdmissionCoordinator, *Repository, *authorization.Repository, contract.Principal, contract.AgentCredentialCreation) {
	t.Helper()
	audits, store, clock := newInvocationRepository(t, fault, entropyBytes(1024))
	authority, err := authorization.New(store, clock, entropyBytes(8192))
	require.NoError(t, err)
	principalCreation, err := authority.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	credential, err := authority.IssueCredential(context.Background(), principalCreation.Principal.ID, principalCreation.Principal.Revision)
	require.NoError(t, err)
	coordinator, err := NewAdmissionCoordinator(audits, authority)
	require.NoError(t, err)
	return coordinator, audits, authority, principalCreation.Principal, credential
}

func testAuditRequest(class contract.InvocationAdmissionClass) AuditAdmissionRequest {
	name := "namespace.tool"
	request := AuditAdmissionRequest{Class: class, RequestedName: &name, RedactedArguments: []byte(`{}`)}
	if class == contract.AdmissionInvalidParams {
		request.RequestedName = nil
		request.RedactedArguments = nil
	}
	if class == contract.AdmissionInvalidArguments || class == contract.AdmissionEvaluated {
		route := testRoute()
		route.ServerID = contract.SyntheticServerID
		request.Route = &route
	}
	if class == contract.AdmissionEvaluated {
		request.Arguments = strictjson.Value{Type: strictjson.ValueObject}
	}
	return request
}

func insertMalformedEvaluationGrant(t *testing.T, store *storage.Store, principalID string) {
	t.Helper()
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`)
		if err != nil {
			return err
		}
		_, err = transaction.ExecContext(context.Background(), `INSERT INTO grants
			(id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at)
			VALUES (?, ?, 'bogus', ?, 'tool', NULL, NULL, ?)`, invocationID(900), principalID, contract.SyntheticServerID, canonicalInvocationTime(invocationTestTime))
		return err
	}))
}

func TestAdmissionImplementationHasOneMutationAndNoNestedRepositoryInsert(t *testing.T) {
	source, err := os.ReadFile("admission.go")
	require.NoError(t, err)
	contents := string(source)
	assert.Equal(t, 1, strings.Count(contents, ".mutate("))
	assert.NotContains(t, contents, ".Insert(")
	assert.Contains(t, contents, ".InsertTx(")
}
