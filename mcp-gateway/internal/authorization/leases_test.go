package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticateReturnsRegisteredPendingLeaseWithIdempotentRelease(t *testing.T) {
	repository, bearer := issuedLeaseRepository(t)
	lease, err := repository.Authenticate(context.Background(), bearer)
	require.NoError(t, err)
	assert.Equal(t, leasePending, leasePhase(lease.phase.Load()))
	assert.True(t, lease.Current())
	assert.Equal(t, 1, repository.authority.count())
	select {
	case <-lease.Done():
		t.Fatal("new lease is already done")
	default:
	}

	const releasers = 32
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(releasers)
	for range releasers {
		go func() {
			defer group.Done()
			<-start
			lease.Release()
		}()
	}
	close(start)
	group.Wait()
	select {
	case <-lease.Done():
	default:
		t.Fatal("released lease did not close")
	}
	assert.False(t, lease.Current())
	assert.Equal(t, 0, repository.authority.count())
	lease.Release()
}

func TestDrainOrderedBeforeRegistrationRejectsAuthentication(t *testing.T) {
	repository, bearer := issuedLeaseRepository(t)
	bindingRead := make(chan struct{})
	releaseAuthentication := make(chan struct{})
	drainFenced := make(chan struct{})
	repository.authority.hooks.afterBindingRead = func() {
		close(bindingRead)
		<-releaseAuthentication
	}
	repository.authority.hooks.afterDrainFence = func() { close(drainFenced) }

	authResult := make(chan leaseResult, 1)
	go func() {
		lease, err := repository.Authenticate(context.Background(), bearer)
		authResult <- leaseResult{lease: lease, err: err}
	}()
	<-bindingRead
	drainResult := make(chan error, 1)
	go func() { drainResult <- repository.Drain(context.Background()) }()
	<-drainFenced
	close(releaseAuthentication)

	result := <-authResult
	assert.ErrorIs(t, result.err, ErrShuttingDown)
	assert.Nil(t, result.lease)
	require.NoError(t, <-drainResult)
	assert.Equal(t, 0, repository.authority.count())
}

func TestRegistrationOrderedBeforeDrainReturnsThenCancelsLease(t *testing.T) {
	repository, bearer := issuedLeaseRepository(t)
	registered := make(chan struct{})
	releaseAuthentication := make(chan struct{})
	drainFenced := make(chan struct{})
	repository.authority.hooks.afterLeaseRegister = func() {
		close(registered)
		<-releaseAuthentication
	}
	repository.authority.hooks.afterDrainFence = func() { close(drainFenced) }

	authResult := make(chan leaseResult, 1)
	go func() {
		lease, err := repository.Authenticate(context.Background(), bearer)
		authResult <- leaseResult{lease: lease, err: err}
	}()
	<-registered
	drainResult := make(chan error, 1)
	go func() { drainResult <- repository.Drain(context.Background()) }()
	<-drainFenced
	select {
	case <-drainResult:
		t.Fatal("drain passed an occupied authority gate")
	default:
	}
	close(releaseAuthentication)

	result := <-authResult
	require.NoError(t, result.err)
	require.NotNil(t, result.lease)
	require.NoError(t, <-drainResult)
	select {
	case <-result.lease.Done():
	default:
		t.Fatal("drain did not cancel registered lease")
	}
	assert.False(t, result.lease.Current())
	assert.Equal(t, leaseCancelled, leasePhase(result.lease.phase.Load()))
	assert.Equal(t, 0, repository.authority.count())
	result.lease.Release()
}

func TestAuthorityGateRejectsWithoutWaiters(t *testing.T) {
	repository, bearer := issuedLeaseRepository(t)
	gateAcquired := make(chan struct{})
	releaseFirst := make(chan struct{})
	repository.authority.hooks.afterGateAcquire = func() {
		close(gateAcquired)
		<-releaseFirst
	}
	firstResult := make(chan leaseResult, 1)
	go func() {
		lease, err := repository.Authenticate(context.Background(), bearer)
		firstResult <- leaseResult{lease: lease, err: err}
	}()
	<-gateAcquired

	const rejected = 32
	results := make(chan error, rejected)
	for range rejected {
		go func() {
			_, err := repository.Authenticate(context.Background(), bearer)
			results <- err
		}()
	}
	for range rejected {
		assert.ErrorIs(t, <-results, ErrResourceLimit)
	}
	assert.Equal(t, 0, repository.authority.count())
	close(releaseFirst)
	first := <-firstResult
	require.NoError(t, first.err)
	require.NotNil(t, first.lease)
	first.lease.Release()
	assert.Equal(t, 0, repository.authority.count())
}

func TestAuthenticateCancellationAfterReadRegistersNoLease(t *testing.T) {
	repository, bearer := issuedLeaseRepository(t)
	bindingRead := make(chan struct{})
	releaseAuthentication := make(chan struct{})
	repository.authority.hooks.afterBindingRead = func() {
		close(bindingRead)
		<-releaseAuthentication
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan leaseResult, 1)
	go func() {
		lease, err := repository.Authenticate(ctx, bearer)
		result <- leaseResult{lease: lease, err: err}
	}()
	<-bindingRead
	cancel()
	close(releaseAuthentication)
	actual := <-result
	assert.ErrorIs(t, actual.err, context.Canceled)
	assert.Nil(t, actual.lease)
	assert.Equal(t, 0, repository.authority.count())
}

func TestDrainDeadlineLeavesFenceAndLaterCompletes(t *testing.T) {
	repository, bearer := issuedLeaseRepository(t)
	gateAcquired := make(chan struct{})
	releaseAuthentication := make(chan struct{})
	drainFenced := make(chan struct{})
	repository.authority.hooks.afterGateAcquire = func() {
		close(gateAcquired)
		<-releaseAuthentication
	}
	repository.authority.hooks.afterDrainFence = func() {
		select {
		case <-drainFenced:
		default:
			close(drainFenced)
		}
	}
	authResult := make(chan leaseResult, 1)
	go func() {
		lease, err := repository.Authenticate(context.Background(), bearer)
		authResult <- leaseResult{lease: lease, err: err}
	}()
	<-gateAcquired
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, repository.Drain(ctx), context.Canceled)
	<-drainFenced
	assert.True(t, repository.authority.draining.Load())
	close(releaseAuthentication)
	auth := <-authResult
	assert.ErrorIs(t, auth.err, ErrShuttingDown)
	assert.Nil(t, auth.lease)
	require.NoError(t, repository.Drain(context.Background()))
	lease, err := repository.Authenticate(context.Background(), bearer)
	assert.ErrorIs(t, err, ErrShuttingDown)
	assert.Nil(t, lease)
}

func TestDrainCleansAllPendingLeasesWithoutInternalGoroutines(t *testing.T) {
	repository, bearer := issuedLeaseRepository(t)
	leases := make([]*Lease, 32)
	for index := range leases {
		lease, err := repository.Authenticate(context.Background(), bearer)
		require.NoError(t, err)
		leases[index] = lease
	}
	assert.Equal(t, len(leases), repository.authority.count())
	require.NoError(t, repository.Drain(context.Background()))
	assert.Equal(t, 0, repository.authority.count())
	for _, lease := range leases {
		select {
		case <-lease.Done():
		default:
			t.Fatal("drain left a pending lease open")
		}
		assert.False(t, lease.Current())
		lease.Release()
	}
	require.NoError(t, repository.Drain(context.Background()))

	source, err := os.ReadFile("leases.go")
	require.NoError(t, err)
	assert.NotContains(t, string(source), "go func")
	assert.NotContains(t, string(source), "time.")
}

func TestLeaseCurrentFailsClosedOnStorageLatch(t *testing.T) {
	armed := false
	repository, store, ownership := newFaultCredentialRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("latch")
		}
		return nil
	})
	principal := mustCreatePrincipal(t, repository)
	creation, err := repository.IssueCredential(context.Background(), principal.ID, "1")
	require.NoError(t, err)
	lease, err := repository.Authenticate(context.Background(), creation.Bearer)
	require.NoError(t, err)
	armed = true
	assert.ErrorIs(t, store.Mutate(context.Background(), func(*sql.Tx) error { return nil }), storage.ErrStorageLatched)
	assert.False(t, lease.Current())
	lease.Release()
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func TestLeaseBindingIsSafeAndImmutable(t *testing.T) {
	repository, bearer := issuedLeaseRepository(t)
	lease, err := repository.Authenticate(context.Background(), bearer)
	require.NoError(t, err)
	binding := lease.Binding()
	binding.PrincipalID = "changed"
	assert.NotEqual(t, binding, lease.Binding())
	encoded, err := json.Marshal(lease.Binding())
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), bearer)
	lease.Release()
}

type leaseResult struct {
	lease *Lease
	err   error
}

func (registry *authorityRegistry) count() int {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.leases)
}

func issuedLeaseRepository(t *testing.T) (*Repository, string) {
	t.Helper()
	repository, _ := newRepository(t, nil)
	principal, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	creation, err := repository.IssueCredential(context.Background(), principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	return repository, creation.Bearer
}
