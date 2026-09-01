package authorization

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEveryAuthorityMutationOrdersBothWaysWithAuthentication(t *testing.T) {
	armed := false
	var mutationEntered, releaseMutation chan struct{}
	repository, _ := newRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultArmDirectorySync {
			close(mutationEntered)
			<-releaseMutation
		}
		return nil
	})
	for _, test := range authorityMutationCases() {
		t.Run(test.name, func(t *testing.T) {
			bearer, mutate := test.prepare(t, repository)

			gateAcquired := make(chan struct{})
			releaseGate := make(chan struct{})
			repository.authority.hooks.afterGateAcquire = func() {
				close(gateAcquired)
				<-releaseGate
			}
			authResult := make(chan leaseResult, 1)
			go func() {
				lease, err := repository.Authenticate(context.Background(), bearer)
				authResult <- leaseResult{lease: lease, err: err}
			}()
			<-gateAcquired
			assert.ErrorIs(t, mutate(), ErrResourceLimit)
			close(releaseGate)
			result := <-authResult
			require.NoError(t, result.err)
			result.lease.Release()

			repository.authority.hooks = authorityHooks{}
			mutationEntered = make(chan struct{})
			releaseMutation = make(chan struct{})
			armed = true
			mutationResult := make(chan error, 1)
			go func() { mutationResult <- mutate() }()
			<-mutationEntered
			lease, err := repository.Authenticate(context.Background(), bearer)
			assert.ErrorIs(t, err, ErrResourceLimit)
			assert.Nil(t, lease)
			close(releaseMutation)
			require.NoError(t, <-mutationResult)
			armed = false
		})
	}
	require.NoError(t, repository.Drain(context.Background()))
	_, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "After drain", Visibility: contract.VisibilityRequestable})
	assert.ErrorIs(t, err, ErrShuttingDown)
}

func TestPrincipalAndCredentialCommitsCancelOnlyAffectedLeases(t *testing.T) {
	armed := false
	var committed, releaseCommit chan struct{}
	repository, _ := newRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			close(committed)
			<-releaseCommit
		}
		return nil
	})
	for _, test := range []struct {
		name   string
		mutate func(*Repository, contract.AgentCredentialCreation) error
	}{
		{name: "principal patch", mutate: func(repository *Repository, issued contract.AgentCredentialCreation) error {
			displayName := "Changed"
			_, err := repository.PatchPrincipal(context.Background(), issued.Principal.ID, PatchPrincipalRequest{ExpectedRevision: issued.Principal.Revision, DisplayName: &displayName})
			return err
		}},
		{name: "credential replace", mutate: func(repository *Repository, issued contract.AgentCredentialCreation) error {
			_, err := repository.IssueCredential(context.Background(), issued.Principal.ID, issued.Principal.Revision)
			return err
		}},
		{name: "credential revoke", mutate: func(repository *Repository, issued contract.AgentCredentialCreation) error {
			_, err := repository.RevokeCredential(context.Background(), issued.Principal.ID, issued.Principal.Revision)
			return err
		}},
		{name: "principal disable", mutate: func(repository *Repository, issued contract.AgentCredentialCreation) error {
			disabled := contract.PrincipalDisabled
			_, err := repository.PatchPrincipal(context.Background(), issued.Principal.ID, PatchPrincipalRequest{ExpectedRevision: issued.Principal.Revision, State: &disabled})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			committed = make(chan struct{})
			releaseCommit = make(chan struct{})
			target := mustCreatePrincipal(t, repository)
			targetCredential, err := repository.IssueCredential(context.Background(), target.ID, target.Revision)
			require.NoError(t, err)
			control := mustCreatePrincipal(t, repository)
			controlCredential, err := repository.IssueCredential(context.Background(), control.ID, control.Revision)
			require.NoError(t, err)
			targetLease := mustAuthenticateLease(t, repository, targetCredential.Bearer)
			controlLease := mustAuthenticateLease(t, repository, controlCredential.Bearer)
			armed = true
			mutationResult := make(chan error, 1)
			go func() { mutationResult <- test.mutate(repository, targetCredential) }()
			<-committed
			assertLeaseOpen(t, targetLease)
			assertLeaseOpen(t, controlLease)
			close(releaseCommit)
			require.NoError(t, <-mutationResult)
			assertLeaseClosed(t, targetLease)
			assertLeaseOpen(t, controlLease)
			controlLease.Release()
			armed = false
		})
	}
}

func TestGrantMutationsDoNotCancelCredentialLeases(t *testing.T) {
	armed := false
	committed := make(chan struct{}, 2)
	releaseCommit := make(chan struct{}, 2)
	repository, _ := newRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			committed <- struct{}{}
			<-releaseCommit
		}
		return nil
	})
	principal := mustCreatePrincipal(t, repository)
	credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
	require.NoError(t, err)
	lease := mustAuthenticateLease(t, repository, credential.Bearer)
	armed = true
	created := make(chan contract.Grant, 1)
	mutationResult := make(chan error, 1)
	go func() {
		grant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
		created <- grant
		mutationResult <- err
	}()
	<-committed
	assertLeaseOpen(t, lease)
	releaseCommit <- struct{}{}
	require.NoError(t, <-mutationResult)
	grant := <-created
	assertLeaseOpen(t, lease)

	go func() { mutationResult <- repository.DeleteGrant(context.Background(), grant.ID) }()
	<-committed
	assertLeaseOpen(t, lease)
	releaseCommit <- struct{}{}
	require.NoError(t, <-mutationResult)
	assertLeaseOpen(t, lease)
	lease.Release()
}

func TestUncertainPrincipalAndCredentialMutationsCancelAffectedLeases(t *testing.T) {
	for _, name := range []string{"principal patch", "credential replace"} {
		t.Run(name, func(t *testing.T) {
			armed := false
			repository, store := newRepository(t, func(point storage.FaultPoint) error {
				if armed && point == storage.FaultAfterCommit {
					return errors.New("acknowledgement lost")
				}
				return nil
			})
			principal := mustCreatePrincipal(t, repository)
			credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
			require.NoError(t, err)
			lease := mustAuthenticateLease(t, repository, credential.Bearer)
			armed = true
			if name == "principal patch" {
				display := "Changed"
				_, err = repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, DisplayName: &display})
			} else {
				_, err = repository.IssueCredential(context.Background(), principal.ID, credential.Principal.Revision)
			}
			assert.ErrorIs(t, err, ErrStorageUnavailable)
			assert.True(t, store.Latched())
			assertLeaseClosed(t, lease)
			assert.Equal(t, 0, repository.authority.count())
		})
	}
}

func TestUncertainGrantMutationDoesNotCloseCredentialChannel(t *testing.T) {
	armed := false
	repository, store := newRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("acknowledgement lost")
		}
		return nil
	})
	principal := mustCreatePrincipal(t, repository)
	credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
	require.NoError(t, err)
	lease := mustAuthenticateLease(t, repository, credential.Bearer)
	armed = true
	_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.True(t, store.Latched())
	assertLeaseOpen(t, lease)
	assert.False(t, lease.Current())
	lease.Release()
}

func TestAuthorityGateAndStorageMutationAdmissionNeverWaitOnEachOther(t *testing.T) {
	t.Run("storage first", func(t *testing.T) {
		repository, store := newRepository(t, nil)
		principal := mustCreatePrincipal(t, repository)
		credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
		require.NoError(t, err)
		storageEntered := make(chan struct{})
		releaseStorage := make(chan struct{})
		storageResult := make(chan error, 1)
		go func() {
			storageResult <- store.Mutate(context.Background(), func(*sql.Tx) error {
				close(storageEntered)
				<-releaseStorage
				return nil
			})
		}()
		<-storageEntered
		display := "Changed"
		_, err = repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, DisplayName: &display})
		assert.ErrorIs(t, err, ErrResourceLimit)
		lease, err := repository.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err, "storage rejection must release the authority gate")
		lease.Release()
		close(releaseStorage)
		require.NoError(t, <-storageResult)
	})

	t.Run("authority first", func(t *testing.T) {
		armed := false
		mutationEntered := make(chan struct{})
		releaseMutation := make(chan struct{})
		repository, store := newRepository(t, func(point storage.FaultPoint) error {
			if armed && point == storage.FaultArmDirectorySync {
				close(mutationEntered)
				<-releaseMutation
			}
			return nil
		})
		principal := mustCreatePrincipal(t, repository)
		credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
		require.NoError(t, err)
		armed = true
		mutationResult := make(chan error, 1)
		go func() {
			display := "Changed"
			_, err := repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, DisplayName: &display})
			mutationResult <- err
		}()
		<-mutationEntered
		assert.ErrorIs(t, store.Mutate(context.Background(), func(*sql.Tx) error { return nil }), storage.ErrMutationBusy)
		_, err = repository.Authenticate(context.Background(), credential.Bearer)
		assert.ErrorIs(t, err, ErrResourceLimit)
		close(releaseMutation)
		require.NoError(t, <-mutationResult)
	})
}

type authorityMutationCase struct {
	name    string
	prepare func(*testing.T, *Repository) (string, func() error)
}

func authorityMutationCases() []authorityMutationCase {
	return []authorityMutationCase{
		{name: "principal create", prepare: func(t *testing.T, repository *Repository) (string, func() error) {
			bearer := createBearer(t, repository)
			return bearer, func() error {
				_, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "New", Visibility: contract.VisibilityRequestable})
				return err
			}
		}},
		{name: "principal patch", prepare: func(t *testing.T, repository *Repository) (string, func() error) {
			principal := mustCreatePrincipal(t, repository)
			credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
			require.NoError(t, err)
			return credential.Bearer, func() error {
				display := "Changed"
				_, err := repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, DisplayName: &display})
				return err
			}
		}},
		{name: "credential issue", prepare: func(t *testing.T, repository *Repository) (string, func() error) {
			bearer := createBearer(t, repository)
			principal := mustCreatePrincipal(t, repository)
			return bearer, func() error {
				_, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
				return err
			}
		}},
		{name: "credential replace", prepare: credentialMutationCase(func(repository *Repository, credential contract.AgentCredentialCreation) error {
			_, err := repository.IssueCredential(context.Background(), credential.Principal.ID, credential.Principal.Revision)
			return err
		})},
		{name: "credential revoke", prepare: credentialMutationCase(func(repository *Repository, credential contract.AgentCredentialCreation) error {
			_, err := repository.RevokeCredential(context.Background(), credential.Principal.ID, credential.Principal.Revision)
			return err
		})},
		{name: "principal disable", prepare: credentialMutationCase(func(repository *Repository, credential contract.AgentCredentialCreation) error {
			disabled := contract.PrincipalDisabled
			_, err := repository.PatchPrincipal(context.Background(), credential.Principal.ID, PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, State: &disabled})
			return err
		})},
		{name: "principal re-enable", prepare: func(t *testing.T, repository *Repository) (string, func() error) {
			bearer := createBearer(t, repository)
			principal := mustCreatePrincipal(t, repository)
			disabled := contract.PrincipalDisabled
			updated, err := repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: principal.Revision, State: &disabled})
			require.NoError(t, err)
			return bearer, func() error {
				active := contract.PrincipalActive
				_, err := repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: updated.Revision, State: &active})
				return err
			}
		}},
		{name: "grant create", prepare: credentialMutationCase(func(repository *Repository, credential contract.AgentCredentialCreation) error {
			_, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: credential.Principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
			return err
		})},
		{name: "grant delete", prepare: func(t *testing.T, repository *Repository) (string, func() error) {
			principal := mustCreatePrincipal(t, repository)
			credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
			require.NoError(t, err)
			grant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
			require.NoError(t, err)
			return credential.Bearer, func() error { return repository.DeleteGrant(context.Background(), grant.ID) }
		}},
	}
}

func credentialMutationCase(mutate func(*Repository, contract.AgentCredentialCreation) error) func(*testing.T, *Repository) (string, func() error) {
	return func(t *testing.T, repository *Repository) (string, func() error) {
		principal := mustCreatePrincipal(t, repository)
		credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
		require.NoError(t, err)
		return credential.Bearer, func() error { return mutate(repository, credential) }
	}
}

func createBearer(t *testing.T, repository *Repository) string {
	t.Helper()
	principal := mustCreatePrincipal(t, repository)
	credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
	require.NoError(t, err)
	return credential.Bearer
}

func mustAuthenticateLease(t *testing.T, repository *Repository, bearer string) *Lease {
	t.Helper()
	lease, err := repository.Authenticate(context.Background(), bearer)
	require.NoError(t, err)
	return lease
}

func assertLeaseOpen(t *testing.T, lease *Lease) {
	t.Helper()
	select {
	case <-lease.Done():
		t.Fatal("lease closed")
	default:
	}
}

func assertLeaseClosed(t *testing.T, lease *Lease) {
	t.Helper()
	select {
	case <-lease.Done():
	default:
		t.Fatal("lease remained open")
	}
}
