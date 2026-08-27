package grantrequests

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const requestTestInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var requestTestTime = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

func TestS5RequestCreateServerAndToolPersistsBoundedEvidenceAndInvalidates(t *testing.T) {
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	descriptors := &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{
		"sample.echo": requestDescriptor(t, requestID(400), requestID(401), "sample", "echo", contract.EvidenceCurrent),
	}}
	denies := new(fakeDenyInspector)
	clock := &countingRequestClock{now: requestTestTime}
	var invalidations []contract.Invalidation
	repository, store := newRequestRepository(t, requestRepositoryOptions{
		clock: clock, namespaces: namespaces, descriptors: descriptors, denies: denies,
		invalidate: func(event contract.Invalidation) { invalidations = append(invalidations, event) },
	})

	serverResult, err := repository.CreateOrExisting(context.Background(), CreateRequest{
		PrincipalID: requestID(200), Policy: contract.Policy{
			Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestCreated, serverResult.Outcome)
	require.NotNil(t, serverResult.Request)
	assert.Equal(t, contract.RequestPending, serverResult.Request.State)
	assert.Equal(t, "1", serverResult.Request.Revision)
	assert.Nil(t, serverResult.Request.ApprovedPolicy)

	constraint := json.RawMessage(`{"equals":{"/region":"west"}}`)
	toolResult, err := repository.CreateOrExisting(context.Background(), CreateRequest{
		PrincipalID: requestID(200), Policy: contract.Policy{
			Scope: contract.PolicyTool, Target: "sample.echo", Constraint: &constraint,
			DurationSeconds: stringPointer("60"), FutureToolsAcknowledged: false,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestCreated, toolResult.Outcome)
	require.NotNil(t, toolResult.Request)
	assert.Equal(t, "sample.echo", toolResult.Request.RequestedPolicy.Target)
	assert.Equal(t, 2, clock.calls)
	assert.Equal(t, 1, descriptors.calls)
	assert.Equal(t, 2, denies.calls)
	assert.Equal(t, []contract.Invalidation{{Kind: contract.InvalidationGrantRequests}, {Kind: contract.InvalidationGrantRequests}}, invalidations)

	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		var identities, rows, evidenceBytes, authorizationRevision int64
		require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_request_identities`).Scan(&identities))
		require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests`).Scan(&rows))
		require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT total_bytes FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(&evidenceBytes))
		require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT revision FROM authorization_meta WHERE singleton = 1`).Scan(&authorizationRevision))
		assert.Equal(t, int64(2), identities)
		assert.Equal(t, int64(2), rows)
		assert.Positive(t, evidenceBytes)
		assert.Zero(t, authorizationRevision)
		var evidence []byte
		require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT submitted_evidence FROM grant_requests WHERE id = ?`, toolResult.Request.ID).Scan(&evidence))
		assert.LessOrEqual(t, int64(len(evidence)), fixedLimit("grant_request_evidence_snapshot_bytes"))
		return nil
	}))
}

func TestS5SemanticDedupeWinsBeforeDeletedTargetDenyAndCapacity(t *testing.T) {
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	descriptors := &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{
		"sample.echo": requestDescriptor(t, requestID(400), requestID(401), "sample", "echo", contract.EvidenceRetired),
	}}
	denies := new(fakeDenyInspector)
	clock := &countingRequestClock{now: requestTestTime}
	invalidations := 0
	repository, store := newRequestRepository(t, requestRepositoryOptions{
		clock: clock, namespaces: namespaces, descriptors: descriptors, denies: denies,
		invalidate: func(contract.Invalidation) { invalidations++ },
	})
	leftConstraint := json.RawMessage(`{"equals":{"/b":true,"/a":1.0}}`)
	rightConstraint := json.RawMessage(`{"equals":{"/a":1.0,"/b":true}}`)
	left := CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyTool, Target: "sample.echo", Constraint: &leftConstraint,
	}}
	created, err := repository.CreateOrExisting(context.Background(), left)
	require.NoError(t, err)
	assert.Equal(t, contract.RequestCreated, created.Outcome)

	namespaces.targets["sample"] = servers.NamespaceTarget{ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDeleted}
	denies.conflict = true
	existing, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: left.PrincipalID, Policy: contract.Policy{
		Scope: contract.PolicyTool, Target: "sample.echo", Constraint: &rightConstraint,
	}})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestExisting, existing.Outcome)
	require.NotNil(t, existing.Request)
	assert.Equal(t, created.Request.ID, existing.Request.ID)
	assert.JSONEq(t, string(leftConstraint), string(*existing.Request.RequestedPolicy.Constraint))
	assert.Equal(t, 1, clock.calls, "existing lookup must not capture a new timestamp")
	assert.Equal(t, 1, descriptors.calls, "existing lookup must not refresh evidence")
	assert.Equal(t, 1, denies.calls, "existing lookup must not recheck DENY")
	assert.Equal(t, 1, invalidations)

	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		var rows, identities int
		require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests`).Scan(&rows))
		require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_request_identities`).Scan(&identities))
		assert.Equal(t, 1, rows)
		assert.Equal(t, 1, identities)
		return nil
	}))
}

func TestS5SemanticDedupeConcurrentSubmissionNeverQueuesOrDuplicates(t *testing.T) {
	base := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	blocking := &blockingNamespaceInspector{delegate: base, entered: make(chan struct{}), release: make(chan struct{})}
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, requestTestInstallationID)
	require.NoError(t, err)
	repository, err := New(Options{
		Store: store, Clock: &countingRequestClock{now: requestTestTime}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x66}, 20)),
		Namespaces: blocking, Descriptors: &fakeDescriptorInspector{}, Denies: new(fakeDenyInspector),
		Invalidate: func(contract.Invalidation) {},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(); _ = ownership.Close() })
	request := CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}}
	type response struct {
		result contract.CreateGrantRequestResult
		err    error
	}
	first := make(chan response, 1)
	go func() {
		result, createErr := repository.CreateOrExisting(context.Background(), request)
		first <- response{result: result, err: createErr}
	}()
	<-blocking.entered
	_, err = repository.CreateOrExisting(context.Background(), request)
	require.ErrorIs(t, err, ErrStorageUnavailable, "mutation admission must reject rather than queue")
	close(blocking.release)
	created := <-first
	require.NoError(t, created.err)
	assert.Equal(t, contract.RequestCreated, created.result.Outcome)

	existing, err := repository.CreateOrExisting(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, contract.RequestExisting, existing.Outcome)
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		var rows int
		require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT count(*) FROM grant_requests`).Scan(&rows))
		assert.Equal(t, 1, rows)
		return nil
	}))
}

func TestS5RequestCreateReturnsClosedTargetDenyAndValidationOutcomes(t *testing.T) {
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample":  {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
		"deleted": {ID: requestID(402), Namespace: "deleted", State: contract.DesiredServerDeleted},
	}}
	descriptors := &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{}}
	denies := &fakeDenyInspector{conflict: true}
	repository, _ := newRequestRepository(t, requestRepositoryOptions{
		clock: &countingRequestClock{now: requestTestTime}, namespaces: namespaces, descriptors: descriptors, denies: denies,
		invalidate: func(contract.Invalidation) {},
	})

	result, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestDenyConflict, result.Outcome)
	assert.Nil(t, result.Request)

	denies.conflict = false
	result, err = repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "deleted", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestTargetUnavailable, result.Outcome)

	result, err = repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyTool, Target: "sample.missing",
	}})
	require.NoError(t, err)
	assert.Equal(t, contract.RequestTargetUnavailable, result.Outcome)

	_, err = repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: "malformed", Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.ErrorIs(t, err, ErrInvalidInput)
	_, err = repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyTool, Target: "invalid target",
	}})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestS5RequestCreatePostCommitUncertaintyReturnsUnavailableWithoutInvalidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	faulted := false
	store, err := storage.InitializeWithFaultInjection(context.Background(), ownership, requestTestInstallationID, func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit && !faulted {
			faulted = true
			return assert.AnError
		}
		return nil
	})
	require.NoError(t, err)
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	invalidations := 0
	repository, err := New(Options{
		Store: store, Clock: &countingRequestClock{now: requestTestTime}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x44}, 10)),
		Namespaces: namespaces, Descriptors: &fakeDescriptorInspector{}, Denies: new(fakeDenyInspector),
		Invalidate: func(contract.Invalidation) { invalidations++ },
	})
	require.NoError(t, err)
	request := CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}}
	_, err = repository.CreateOrExisting(context.Background(), request)
	require.ErrorIs(t, err, ErrStorageUnavailable)
	assert.True(t, store.Latched())
	assert.Zero(t, invalidations)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
	_, err = storage.VerifyCurrent(context.Background(), root)
	require.NoError(t, err)

	ownership, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err = storage.Open(context.Background(), ownership)
	require.NoError(t, err)
	repository, err = New(Options{
		Store: store, Clock: &countingRequestClock{now: requestTestTime}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x55}, 10)),
		Namespaces: namespaces, Descriptors: &fakeDescriptorInspector{}, Denies: new(fakeDenyInspector),
		Invalidate: func(contract.Invalidation) { invalidations++ },
	})
	require.NoError(t, err)
	result, err := repository.CreateOrExisting(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, contract.RequestExisting, result.Outcome)
	assert.Zero(t, invalidations)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func TestS5RequestCreatePermanentIdentityRejectsReuseAfterEviction(t *testing.T) {
	namespaces := &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{
		"sample": {ID: requestID(400), Namespace: "sample", State: contract.DesiredServerDisabled},
	}}
	entropy := bytes.NewReader(bytes.Repeat([]byte{0x11}, 20))
	repository, store := newRequestRepository(t, requestRepositoryOptions{
		clock: &countingRequestClock{now: requestTestTime}, entropy: entropy, namespaces: namespaces,
		descriptors: &fakeDescriptorInspector{}, denies: new(fakeDenyInspector), invalidate: func(contract.Invalidation) {},
	})
	created, err := repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(200), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, transitionErr := transaction.ExecContext(context.Background(), `UPDATE grant_requests SET state = 'cancelled', revision = 2, updated_at = ?, closed_at = ? WHERE id = ?`, requestTimestamp(requestTestTime.Add(time.Second)), requestTimestamp(requestTestTime.Add(time.Second)), created.Request.ID)
		if transitionErr != nil {
			return transitionErr
		}
		_, deleteErr := transaction.ExecContext(context.Background(), `DELETE FROM grant_requests WHERE id = ?`, created.Request.ID)
		return deleteErr
	}))

	_, err = repository.CreateOrExisting(context.Background(), CreateRequest{PrincipalID: requestID(201), Policy: contract.Policy{
		Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true,
	}})
	require.ErrorIs(t, err, ErrIdentityUnavailable)
}

type requestRepositoryOptions struct {
	clock       *countingRequestClock
	entropy     *bytes.Reader
	namespaces  *fakeNamespaceInspector
	descriptors *fakeDescriptorInspector
	denies      *fakeDenyInspector
	invalidate  func(contract.Invalidation)
}

func newRequestRepository(t *testing.T, options requestRepositoryOptions) (*Repository, *storage.Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, requestTestInstallationID)
	require.NoError(t, err)
	if options.clock == nil {
		options.clock = &countingRequestClock{now: requestTestTime}
	}
	if options.namespaces == nil {
		options.namespaces = &fakeNamespaceInspector{targets: map[string]servers.NamespaceTarget{}}
	}
	if options.descriptors == nil {
		options.descriptors = &fakeDescriptorInspector{descriptors: map[string]catalog.DurableDescriptor{}}
	}
	if options.denies == nil {
		options.denies = new(fakeDenyInspector)
	}
	if options.invalidate == nil {
		options.invalidate = func(contract.Invalidation) {}
	}
	if options.entropy == nil {
		contents := append(bytes.Repeat([]byte{0x11}, 10), bytes.Repeat([]byte{0x22}, 10)...)
		for fill := byte(0x33); fill < 0x73; fill++ {
			contents = append(contents, bytes.Repeat([]byte{fill}, 10)...)
		}
		options.entropy = bytes.NewReader(contents)
	}
	repository, err := New(Options{
		Store: store, Clock: options.clock, Entropy: options.entropy,
		Namespaces: options.namespaces, Descriptors: options.descriptors, Denies: options.denies,
		Invalidate: options.invalidate,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
		_ = ownership.Close()
	})
	return repository, store
}

type countingRequestClock struct {
	now   time.Time
	calls int
}

func (clock *countingRequestClock) Now() time.Time {
	clock.calls++
	return clock.now
}

type fakeNamespaceInspector struct {
	targets map[string]servers.NamespaceTarget
	calls   int
	err     error
}

func (inspector *fakeNamespaceInspector) LookupNamespaceTargetTx(_ context.Context, _ *sql.Tx, namespace string) (servers.NamespaceTarget, error) {
	inspector.calls++
	if inspector.err != nil {
		return servers.NamespaceTarget{}, inspector.err
	}
	target, found := inspector.targets[namespace]
	if !found {
		return servers.NamespaceTarget{}, servers.ErrNotFound
	}
	return target, nil
}

type blockingNamespaceInspector struct {
	delegate *fakeNamespaceInspector
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (inspector *blockingNamespaceInspector) LookupNamespaceTargetTx(ctx context.Context, transaction *sql.Tx, namespace string) (servers.NamespaceTarget, error) {
	blocked := false
	inspector.once.Do(func() {
		blocked = true
		close(inspector.entered)
	})
	if blocked {
		select {
		case <-inspector.release:
		case <-ctx.Done():
			return servers.NamespaceTarget{}, ctx.Err()
		}
	}
	return inspector.delegate.LookupNamespaceTargetTx(ctx, transaction, namespace)
}

type fakeDescriptorInspector struct {
	descriptors map[string]catalog.DurableDescriptor
	calls       int
	err         error
}

func (inspector *fakeDescriptorInspector) LookupDurableDescriptorTx(_ context.Context, _ *sql.Tx, _ string, externalName string) (catalog.DurableDescriptor, error) {
	inspector.calls++
	if inspector.err != nil {
		return catalog.DurableDescriptor{}, inspector.err
	}
	descriptor, found := inspector.descriptors[externalName]
	if !found {
		return catalog.DurableDescriptor{}, servers.ErrNotFound
	}
	return descriptor, nil
}

type fakeDenyInspector struct {
	conflict bool
	calls    int
	err      error
	at       time.Time
}

func (inspector *fakeDenyInspector) HasActiveDenyConflictTx(_ context.Context, _ *sql.Tx, _ authorization.DenyConflictScope, at time.Time) (bool, error) {
	inspector.calls++
	inspector.at = at
	return inspector.conflict, inspector.err
}

func requestDescriptor(t *testing.T, serverID, toolID, namespace, upstream string, state contract.DescriptorEvidenceState) catalog.DurableDescriptor {
	t.Helper()
	external := namespace + "." + upstream
	normalized, err := catalog.NormalizeTool(catalog.RawTool{
		UpstreamName: upstream, ExternalName: external,
		Descriptor: json.RawMessage(`{"name":"` + upstream + `","inputSchema":{"type":"object"}}`),
	}, catalog.NormalizeOptions{ServerID: serverID, AllowHeaderBindings: true})
	require.NoError(t, err)
	resource := contract.ToolDescriptor{
		ID: toolID, ServerID: serverID, UpstreamName: upstream, ExternalName: external,
		Descriptor: normalized.Descriptor, Fingerprint: normalized.Fingerprint, CatalogRevision: "1",
		FirstSeenAt: requestTimestamp(requestTestTime), LastSeenAt: requestTimestamp(requestTestTime),
	}
	if state == contract.EvidenceRetired {
		retired := requestTimestamp(requestTestTime)
		resource.RetiredAt = &retired
	}
	return catalog.DurableDescriptor{Resource: resource, State: state}
}

func requestID(value int) string {
	return fmt.Sprintf("01J60000000000000000000%03d", value)
}

func requestTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}
