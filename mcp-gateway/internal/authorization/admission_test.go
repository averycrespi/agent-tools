package authorization

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmissionVerifiersAndKnownOutcomeDetachment(t *testing.T) {
	repository, store := newRepository(t, nil)

	t.Run("revision re-evaluation and later policy", func(t *testing.T) {
		principal, credential := createAdmissionCredential(t, repository)
		lease := mustAuthenticateLease(t, repository, credential.Bearer)
		result, token, err := verifyResolvedMutation(repository, store, lease, ResolvedVerification{
			ServerID: contract.SyntheticServerID, UpstreamName: "tool", Arguments: mustAdmissionArguments(t, `{}`),
			ObservedAuthorizationRevision: "0",
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, contract.DecisionAllow, result.Decision)
		assert.Equal(t, "1", result.AuthorizationRevision)
		require.NotNil(t, token)
		assert.Equal(t, leaseAdmitted, leasePhase(lease.phase.Load()))

		pending := mustAuthenticateLease(t, repository, credential.Bearer)
		deny := mustCreateEvaluationGrant(t, repository, CreateGrantRequest{
			PrincipalID: principal.ID, Effect: contract.GrantDeny,
			ServerID: contract.SyntheticServerID, UpstreamName: stringPointer("tool"),
		})
		assertLeaseOpen(t, lease)
		assertLeaseOpen(t, pending)
		result, token, err = verifyResolvedMutation(repository, store, pending, ResolvedVerification{
			ServerID: contract.SyntheticServerID, UpstreamName: "tool", Arguments: mustAdmissionArguments(t, `{}`),
			ObservedAuthorizationRevision: "1",
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, contract.DecisionDeny, result.Decision)
		assert.Equal(t, deny.ID, *result.GrantID)
		assert.Nil(t, token)
		lease.Release()
		pending.Release()
	})

	t.Run("single expiry timestamp and binding-only evidence", func(t *testing.T) {
		principal, credential := createAdmissionCredential(t, repository)
		expiresAt := testNow.Add(time.Second)
		mustCreateEvaluationGrant(t, repository, CreateGrantRequest{
			PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), ExpiresAt: &expiresAt,
		})
		expired := mustAuthenticateLease(t, repository, credential.Bearer)
		repository.clock.(*fixedClock).now = expiresAt
		result, token, err := verifyResolvedMutation(repository, store, expired, ResolvedVerification{
			ServerID: id(51), UpstreamName: "tool", Arguments: mustAdmissionArguments(t, `{}`),
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, contract.DecisionBlock, result.Decision)
		assert.Equal(t, timestamp(expiresAt), result.EvaluatedAt)
		assert.Nil(t, token)
		expired.Release()

		admitted := mustAuthenticateLease(t, repository, credential.Bearer)
		repository.clock.(*fixedClock).now = expiresAt.Add(-time.Nanosecond)
		result, token, err = verifyResolvedMutation(repository, store, admitted, ResolvedVerification{
			ServerID: id(51), UpstreamName: "tool", Arguments: mustAdmissionArguments(t, `{}`),
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, contract.DecisionAllow, result.Decision)
		assert.Equal(t, timestamp(expiresAt.Add(-time.Nanosecond)), result.EvaluatedAt)
		require.NotNil(t, token)
		repository.clock.(*fixedClock).now = expiresAt
		assert.Equal(t, leaseAdmitted, leasePhase(admitted.phase.Load()))
		assertLeaseOpen(t, admitted)

		repository.clock.(*fixedClock).now = testNow
		revision, err := repository.AuthorizationRevision(context.Background())
		require.NoError(t, err)
		bindingLease := mustAuthenticateLease(t, repository, credential.Bearer)
		var evidence BindingVerification
		err = repository.WithAdmission(context.Background(), bindingLease, func(admission *Admission) error {
			return store.Mutate(context.Background(), func(transaction *sql.Tx) error {
				var verifyErr error
				evidence, verifyErr = admission.VerifyBindingOnlyTx(context.Background(), transaction)
				return verifyErr
			})
		})
		require.NoError(t, err)
		assert.Equal(t, BindingVerification{AuthorizationRevision: revision, EvaluatedAt: timestamp(testNow)}, evidence)
		admitted.Release()
		bindingLease.Release()
		repository.clock.(*fixedClock).now = testNow
	})

	t.Run("caller transaction and rollback", func(t *testing.T) {
		_, credential := createAdmissionCredential(t, repository)
		lease := mustAuthenticateLease(t, repository, credential.Bearer)
		err := repository.WithAdmission(context.Background(), lease, func(admission *Admission) error {
			return store.Mutate(context.Background(), func(transaction *sql.Tx) error {
				assert.ErrorIs(t, store.Mutate(context.Background(), func(*sql.Tx) error { return nil }), storage.ErrMutationBusy)
				_, token, _, verifyErr := admission.VerifyResolvedTx(context.Background(), transaction, defaultResolvedVerification())
				require.NoError(t, verifyErr)
				require.NotNil(t, token)
				_, mutationErr := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Nested", Visibility: contract.VisibilityRequestable})
				assert.ErrorIs(t, mutationErr, ErrResourceLimit)
				return nil
			})
		})
		require.NoError(t, err)
		assert.Equal(t, leasePending, leasePhase(lease.phase.Load()))
		lease.Release()

		rollbackLease := mustAuthenticateLease(t, repository, credential.Bearer)
		rollback := errors.New("rollback")
		_, token, err := verifyResolvedMutation(repository, store, rollbackLease, defaultResolvedVerification(), func(*sql.Tx) error { return rollback })
		assert.ErrorIs(t, err, rollback)
		require.NotNil(t, token)
		assert.ErrorIs(t, token.CommitSucceeded(), ErrAdmissionUnavailable)
		assert.Equal(t, leasePending, leasePhase(rollbackLease.phase.Load()))
		rollbackLease.Release()
	})

	t.Run("binding loss and credential invalidation", func(t *testing.T) {
		principal, credential := createAdmissionCredential(t, repository)
		admitted := mustAuthenticateLease(t, repository, credential.Bearer)
		pending := mustAuthenticateLease(t, repository, credential.Bearer)
		resolvedLost := mustAuthenticateLease(t, repository, credential.Bearer)
		bindingLost := mustAuthenticateLease(t, repository, credential.Bearer)
		_, token, err := verifyResolvedMutation(repository, store, admitted, defaultResolvedVerification(), nil)
		require.NoError(t, err)
		require.NotNil(t, token)

		other, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(bytes.Repeat([]byte{0xEE}, 2048)))
		require.NoError(t, err)
		replacement, err := other.IssueCredential(context.Background(), principal.ID, credential.Principal.Revision)
		require.NoError(t, err)
		err = verifyLostResolvedBinding(t, repository, store, resolvedLost)
		assert.ErrorIs(t, err, ErrAuthenticationRequired)
		err = verifyLostBindingOnly(t, repository, store, bindingLost)
		assert.ErrorIs(t, err, ErrAuthenticationRequired)

		_, err = repository.RevokeCredential(context.Background(), principal.ID, replacement.Principal.Revision)
		require.NoError(t, err)
		assertLeaseOpen(t, admitted)
		assertLeaseClosed(t, pending)
		assertLeaseClosed(t, resolvedLost)
		assertLeaseClosed(t, bindingLost)
		assert.Equal(t, leaseAdmitted, leasePhase(admitted.phase.Load()))
		admitted.Release()
	})
}

func TestAdmissionUncertaintyAndUnavailableStorageNeverDetach(t *testing.T) {
	armed := false
	repository, store := newRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("commit acknowledgement lost")
		}
		return nil
	})
	_, credential := createAdmissionCredential(t, repository)
	uncertainLease := mustAuthenticateLease(t, repository, credential.Bearer)
	latchedLease := mustAuthenticateLease(t, repository, credential.Bearer)
	armed = true
	_, token, err := verifyResolvedMutation(repository, store, uncertainLease, defaultResolvedVerification(), nil)
	assert.ErrorIs(t, err, storage.ErrStorageLatched)
	require.NotNil(t, token)
	assert.ErrorIs(t, token.CommitSucceeded(), ErrAdmissionUnavailable)
	assert.Equal(t, leasePending, leasePhase(uncertainLease.phase.Load()))

	err = repository.WithAdmission(context.Background(), latchedLease, func(admission *Admission) error {
		_, _, _, verifyErr := admission.VerifyResolvedTx(context.Background(), nil, defaultResolvedVerification())
		return verifyErr
	})
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.Equal(t, leasePending, leasePhase(latchedLease.phase.Load()))
	uncertainLease.Release()
	latchedLease.Release()
}

func TestAdmissionAddsNoS4PersistenceOrCapabilityUse(t *testing.T) {
	source, err := os.ReadFile("admission.go")
	require.NoError(t, err)
	for _, forbidden := range []string{"CREATE TABLE", "INSERT INTO invocation", "internal/catalog", "internal/downstream", "AcquireRoute", "AcquireCapability", "Dispatch", "Execute"} {
		assert.NotContains(t, string(source), forbidden)
	}
	assert.Equal(t, 10, storage.CurrentSchema)
}

func verifyLostResolvedBinding(t *testing.T, repository *Repository, store *storage.Store, lease *Lease) error {
	t.Helper()
	return repository.WithAdmission(context.Background(), lease, func(admission *Admission) error {
		return store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			_, _, _, err := admission.VerifyResolvedTx(context.Background(), transaction, defaultResolvedVerification())
			return err
		})
	})
}

func verifyLostBindingOnly(t *testing.T, repository *Repository, store *storage.Store, lease *Lease) error {
	t.Helper()
	return repository.WithAdmission(context.Background(), lease, func(admission *Admission) error {
		return store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			_, err := admission.VerifyBindingOnlyTx(context.Background(), transaction)
			return err
		})
	})
}

func verifyResolvedMutation(
	repository *Repository,
	store *storage.Store,
	lease *Lease,
	request ResolvedVerification,
	afterVerify func(*sql.Tx) error,
) (contract.AuthorizationResult, *PendingDetachment, error) {
	var result contract.AuthorizationResult
	var token *PendingDetachment
	err := repository.WithAdmission(context.Background(), lease, func(admission *Admission) error {
		mutationErr := store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			var verifyErr error
			result, token, _, verifyErr = admission.VerifyResolvedTx(context.Background(), transaction, request)
			if verifyErr != nil {
				return verifyErr
			}
			if afterVerify != nil {
				return afterVerify(transaction)
			}
			return nil
		})
		if mutationErr != nil {
			return mutationErr
		}
		if token != nil {
			return token.CommitSucceeded()
		}
		return nil
	})
	return result, token, err
}

func createAdmissionCredential(t *testing.T, repository *Repository) (contract.Principal, contract.AgentCredentialCreation) {
	t.Helper()
	principal := mustCreatePrincipal(t, repository)
	credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
	require.NoError(t, err)
	return principal, credential
}

func defaultResolvedVerification() ResolvedVerification {
	return ResolvedVerification{
		ServerID: contract.SyntheticServerID, UpstreamName: "tool",
		Arguments: strictjson.Value{Type: strictjson.ValueObject},
	}
}

func mustAdmissionArguments(t *testing.T, value string) strictjson.Value {
	t.Helper()
	parsed, err := strictjson.ParseValue([]byte(value), strictjson.Options{MaxBytes: mustLimit("mcp_body_bytes"), MaxDepth: int(mustLimit("json_depth"))})
	require.NoError(t, err)
	return parsed
}
