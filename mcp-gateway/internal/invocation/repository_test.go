package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const invocationTestInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var invocationTestTime = time.Date(2026, 8, 26, 19, 0, 0, 123456789, time.UTC)

type repositoryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *repositoryClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *repositoryClock) Set(now time.Time) {
	clock.mu.Lock()
	clock.now = now
	clock.mu.Unlock()
}

func TestRepositoryPreparesIdentityBeforeTransactionEvidence(t *testing.T) {
	repository, _, clock := newInvocationRepository(t, nil, entropyBytes(32))
	identity, err := repository.PrepareIdentity()
	require.NoError(t, err)
	clock.Set(invocationTestTime.Add(time.Second))
	admission := testEvaluatedAdmission()
	admission.Authorization.EvaluatedAt = canonicalInvocationTime(clock.Now())
	prepared, err := identity.WithAdmission(admission)
	require.NoError(t, err)
	assert.Equal(t, identity.InvocationID, prepared.InvocationID)
	assert.Equal(t, identity.AdmittedAt, prepared.AdmittedAt)
	require.NoError(t, repository.Insert(context.Background(), prepared))
}

func TestRepositoryPublishesCommittedInvocationChanges(t *testing.T) {
	_, store, clock := newInvocationRepository(t, nil, entropyBytes(32))
	var invalidations []contract.Invalidation
	repository, err := NewRepository(store, clock, entropyBytes(32), func(event contract.Invalidation) {
		invalidations = append(invalidations, event)
	})
	require.NoError(t, err)
	prepared, err := repository.Prepare(testEvaluatedAdmission())
	require.NoError(t, err)
	require.NoError(t, repository.Insert(context.Background(), prepared))
	clock.Set(invocationTestTime.Add(time.Second))
	require.NoError(t, repository.AnnotateTerminal(context.Background(), prepared.InvocationID, contract.TerminalSucceeded))
	require.Equal(t, []contract.Invalidation{
		{Kind: contract.InvalidationInvocations, ResourceID: &prepared.InvocationID},
		{Kind: contract.InvalidationInvocations, ResourceID: &prepared.InvocationID},
	}, invalidations)
}

func TestRepositoryInsertsEveryCoherentAdmissionShape(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, entropyBytes(256))
	name, capture := "namespace.tool", []byte(`{"value":1e0}`)
	route := testRoute()
	allow := testAuthorization(contract.DecisionAllow, invocationID(70))
	deny := testAuthorization(contract.DecisionDeny, invocationID(71))
	block := testAuthorization(contract.DecisionBlock, "")
	inputs := []Admission{
		{PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "1", Class: contract.AdmissionInvalidParams},
		{PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "2", Class: contract.AdmissionUnknownTool, RequestedName: &name, RedactedArguments: capture},
		{PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "3", Class: contract.AdmissionInvalidArguments, RequestedName: &name, RedactedArguments: capture, Route: &route},
		{PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "4", Class: contract.AdmissionAuthorizationUnavailable, RequestedName: &name, RedactedArguments: capture, Route: &route},
		{PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "5", Class: contract.AdmissionEvaluated, RequestedName: &name, RedactedArguments: capture, Route: &route, Authorization: &allow},
		{PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "6", Class: contract.AdmissionEvaluated, RequestedName: &name, RedactedArguments: capture, Route: &route, Authorization: &deny},
		{PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "7", Class: contract.AdmissionEvaluated, RequestedName: &name, RedactedArguments: capture, Route: &route, Authorization: &block},
	}
	for index, input := range inputs {
		prepared, err := repository.Prepare(input)
		require.NoError(t, err)
		require.NoError(t, repository.Insert(context.Background(), prepared))
		record, found, err := repository.Read(context.Background(), prepared.InvocationID)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, int64(index+1), record.Sequence)
		assert.Equal(t, prepared.InvocationID, record.InvocationID)
		assert.Equal(t, canonicalInvocationTime(invocationTestTime), record.AdmittedAt)
		assert.Equal(t, input.Class, record.AdmissionClass)
	}
	require.NoError(t, repository.ValidateStartup(context.Background()))
}

func TestRepositoryPrepareRejectsMalformedEvidenceBeforeMutation(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, entropyBytes(128))
	valid := testEvaluatedAdmission()
	badName := ""
	tests := []struct {
		name   string
		mutate func(*Admission)
	}{
		{name: "principal", mutate: func(value *Admission) { value.PrincipalID = "bad" }},
		{name: "credential", mutate: func(value *Admission) { value.CredentialID = "bad" }},
		{name: "fingerprint", mutate: func(value *Admission) { value.CredentialFingerprint = "ABC" }},
		{name: "credential revision", mutate: func(value *Admission) { value.CredentialRevision = "01" }},
		{name: "requested name", mutate: func(value *Admission) { value.RequestedName = &badName }},
		{name: "capture whitespace", mutate: func(value *Admission) { value.RedactedArguments = []byte(`{ "x":1 }`) }},
		{name: "capture duplicate", mutate: func(value *Admission) { value.RedactedArguments = []byte(`{"x":1,"x":2}`) }},
		{name: "partial route", mutate: func(value *Admission) { value.Route.ToolID = "" }},
		{name: "descriptor revision", mutate: func(value *Admission) { value.Route.DescriptorRevision = "0" }},
		{name: "descriptor fingerprint", mutate: func(value *Admission) { value.Route.DescriptorFingerprint = "bad" }},
		{name: "missing authorization", mutate: func(value *Admission) { value.Authorization = nil }},
		{name: "authorization revision", mutate: func(value *Admission) { value.Authorization.AuthorizationRevision = "-1" }},
		{name: "evaluation before admission", mutate: func(value *Admission) {
			value.Authorization.EvaluatedAt = canonicalInvocationTime(invocationTestTime.Add(-time.Nanosecond))
		}},
		{name: "allow without grant", mutate: func(value *Admission) { value.Authorization.GrantID = nil }},
		{name: "block with grant", mutate: func(value *Admission) { value.Authorization.Decision = contract.DecisionBlock }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneAdmission(valid)
			test.mutate(&input)
			_, err := repository.Prepare(input)
			assert.ErrorIs(t, err, ErrInvalidInput)
		})
	}
	count, err := repository.Count(context.Background())
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestRepositoryIdentityFailureCollisionAndClockBounds(t *testing.T) {
	t.Run("entropy", func(t *testing.T) {
		repository, _, _ := newInvocationRepository(t, nil, bytes.NewReader([]byte{1}))
		_, err := repository.Prepare(testEvaluatedAdmission())
		assert.ErrorIs(t, err, ErrIdentityUnavailable)
	})
	for name, now := range map[string]time.Time{
		"clock before epoch":          time.Unix(-1, 0),
		"clock after canonical range": time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		t.Run(name, func(t *testing.T) {
			repository, _, clock := newInvocationRepository(t, nil, entropyBytes(32))
			clock.Set(now)
			_, err := repository.Prepare(testEvaluatedAdmission())
			assert.ErrorIs(t, err, ErrIdentityUnavailable)
		})
	}
	t.Run("collision does not evict", func(t *testing.T) {
		repository, _, _ := newInvocationRepository(t, nil, bytes.NewReader(make([]byte, 20)))
		first, err := repository.Prepare(testEvaluatedAdmission())
		require.NoError(t, err)
		require.NoError(t, repository.Insert(context.Background(), first))
		second, err := repository.Prepare(testEvaluatedAdmission())
		require.NoError(t, err)
		assert.Equal(t, first.InvocationID, second.InvocationID)
		err = repository.Insert(context.Background(), second)
		assert.ErrorIs(t, err, ErrIdentityUnavailable)
		count, countErr := repository.Count(context.Background())
		require.NoError(t, countErr)
		assert.Equal(t, int64(1), count)
	})
}

func TestRepositoryTerminalAnnotationCannotPrecedeEvaluation(t *testing.T) {
	repository, _, clock := newInvocationRepository(t, nil, entropyBytes(64))
	identity, err := repository.PrepareIdentity()
	require.NoError(t, err)
	admission := testEvaluatedAdmission()
	admission.Authorization.EvaluatedAt = canonicalInvocationTime(invocationTestTime.Add(2 * time.Second))
	prepared, err := identity.WithAdmission(admission)
	require.NoError(t, err)
	require.NoError(t, repository.Insert(context.Background(), prepared))
	clock.Set(invocationTestTime.Add(time.Second))
	assert.ErrorIs(t, repository.AnnotateTerminal(context.Background(), prepared.InvocationID, contract.TerminalSucceeded), ErrInvalidInput)
}

func TestRepositoryTerminalAnnotationIsAtMostOnceAndEvictionMissIsBenign(t *testing.T) {
	repository, store, clock := newInvocationRepository(t, nil, entropyBytes(64))
	prepared, err := repository.Prepare(testEvaluatedAdmission())
	require.NoError(t, err)
	require.NoError(t, repository.Insert(context.Background(), prepared))
	clock.Set(invocationTestTime.Add(time.Second))
	require.NoError(t, repository.AnnotateTerminal(context.Background(), prepared.InvocationID, contract.TerminalSucceeded))
	first, found, err := repository.Read(context.Background(), prepared.InvocationID)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, first.CompletedAt)
	assert.Equal(t, canonicalInvocationTime(invocationTestTime.Add(time.Second)), *first.CompletedAt)
	require.NoError(t, repository.AnnotateTerminal(context.Background(), prepared.InvocationID, contract.TerminalOutcomeUnknown))
	second, found, err := repository.Read(context.Background(), prepared.InvocationID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, first.CompletedAt, second.CompletedAt)
	assert.Equal(t, first.TerminalClass, second.TerminalClass)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, deleteErr := transaction.ExecContext(context.Background(), `DELETE FROM invocations WHERE id = ?`, prepared.InvocationID)
		return deleteErr
	}))
	require.NoError(t, repository.AnnotateTerminal(context.Background(), prepared.InvocationID, contract.TerminalSucceeded))
}

func TestRepositoryConcurrentInsertsRemainUniqueAndBounded(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, entropyBytes(16*10))
	prepared := make([]PreparedAdmission, 16)
	for index := range prepared {
		var err error
		prepared[index], err = repository.Prepare(testEvaluatedAdmission())
		require.NoError(t, err)
	}
	start := make(chan struct{})
	errorsByIndex := make([]error, len(prepared))
	var wait sync.WaitGroup
	for index := range prepared {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			errorsByIndex[index] = repository.Insert(context.Background(), prepared[index])
		}(index)
	}
	close(start)
	wait.Wait()
	var successes int64
	for _, err := range errorsByIndex {
		if err == nil {
			successes++
		} else {
			assert.ErrorIs(t, err, ErrStorageUnavailable)
		}
	}
	assert.Positive(t, successes)
	count, err := repository.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, successes, count)
	require.NoError(t, repository.ValidateStartup(context.Background()))
}

func TestRepositoryMutationSaturationRejectsWithoutQueueing(t *testing.T) {
	repository, store, _ := newInvocationRepository(t, nil, entropyBytes(64))
	prepared, err := repository.Prepare(testEvaluatedAdmission())
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.Mutate(context.Background(), func(*sql.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	err = repository.Insert(context.Background(), prepared)
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	close(release)
	require.NoError(t, <-done)
}

func TestRepositoryPostCommitFaultLatchesWithoutRetry(t *testing.T) {
	fault := func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit {
			return errors.New("uncertain commit")
		}
		return nil
	}
	repository, store, _ := newInvocationRepository(t, fault, entropyBytes(64))
	prepared, err := repository.Prepare(testEvaluatedAdmission())
	require.NoError(t, err)
	err = repository.Insert(context.Background(), prepared)
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.True(t, store.Latched())
	_, err = repository.Prepare(testEvaluatedAdmission())
	require.NoError(t, err, "identity preparation remains process-local")
	_, _, err = repository.Read(context.Background(), prepared.InvocationID)
	assert.ErrorIs(t, err, ErrStorageUnavailable)
}

func TestAdmissionInputCannotCarryForbiddenPayloads(t *testing.T) {
	typeOf := reflect.TypeOf(Admission{})
	for _, forbidden := range []string{"Bearer", "Verifier", "DownstreamRequestID", "DispatchStarted", "Result", "RawError", "Retry", "Replay"} {
		_, present := typeOf.FieldByName(forbidden)
		assert.False(t, present, forbidden)
	}
}

func newInvocationRepository(t *testing.T, fault func(storage.FaultPoint) error, entropy io.Reader) (*Repository, *storage.Store, *repositoryClock) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	var store *storage.Store
	if fault == nil {
		store, err = storage.Initialize(context.Background(), ownership, invocationTestInstallationID)
	} else {
		store, err = storage.InitializeWithFaultInjection(context.Background(), ownership, invocationTestInstallationID, fault)
	}
	require.NoError(t, err)
	clock := &repositoryClock{now: invocationTestTime}
	repository, err := NewRepository(store, clock, entropy)
	require.NoError(t, err)
	t.Cleanup(func() {
		if !store.Latched() {
			_ = store.Close()
		}
		_ = ownership.Close()
	})
	return repository, store, clock
}

func testEvaluatedAdmission() Admission {
	name, route := "namespace.tool", testRoute()
	authorization := testAuthorization(contract.DecisionAllow, invocationID(70))
	return Admission{
		PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "1",
		Class: contract.AdmissionEvaluated, RequestedName: &name, RedactedArguments: []byte(`{"value":1e0}`), Route: &route, Authorization: &authorization,
	}
}

func testRoute() RouteEvidence {
	return RouteEvidence{ServerID: invocationID(10), ToolID: invocationID(11), UpstreamName: "tool", DescriptorRevision: "2", DescriptorFingerprint: strings.Repeat("a", 64)}
}

func testAuthorization(decision contract.AuthorizationDecision, grantID string) AuthorizationEvidence {
	result := AuthorizationEvidence{Decision: decision, AuthorizationRevision: "3", EvaluatedAt: canonicalInvocationTime(invocationTestTime)}
	if grantID != "" {
		result.GrantID = &grantID
	}
	return result
}

func cloneAdmission(value Admission) Admission {
	clone := value
	clone.RedactedArguments = append([]byte(nil), value.RedactedArguments...)
	if value.Route != nil {
		route := *value.Route
		clone.Route = &route
	}
	if value.Authorization != nil {
		authorization := *value.Authorization
		if value.Authorization.GrantID != nil {
			grant := *value.Authorization.GrantID
			authorization.GrantID = &grant
		}
		clone.Authorization = &authorization
	}
	return clone
}

func invocationID(sequence int) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	value := []byte("01J60000000000000000000000")
	for index := 25; index >= 22; index-- {
		value[index] = alphabet[sequence%32]
		sequence /= 32
	}
	return string(value)
}

func entropyBytes(size int) io.Reader {
	value := make([]byte, size)
	for index := range value {
		value[index] = byte(index%251 + 1)
	}
	return bytes.NewReader(value)
}
