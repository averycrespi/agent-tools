package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestMatcherCacheColdConcurrencyAndExhaustion(t *testing.T) {
	repository := &Repository{}
	entered := make(chan string, maxCompiledConstraintFlights)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var compilations atomic.Int64
	repository.constraintCompiler = func(source []byte) (CompiledConstraint, error) {
		compilations.Add(1)
		entered <- string(source)
		<-release
		return CompileConstraint(source)
	}
	results := make(chan error, 8)
	source := func(index int) string { return fmt.Sprintf(`{"version":2,"regex":{"/x":"item/%d"}}`, index) }
	for index := range maxCompiledConstraintFlights {
		go func() { _, err := repository.compileConstraint(fmt.Sprint(index+2), source(index)); results <- err }()
	}
	for range maxCompiledConstraintFlights {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("unrelated cold compilation serialized")
		}
	}
	_, err := repository.compileConstraint("99", source(5))
	require.ErrorIs(t, err, ErrResourceLimit)
	repository.constraintCache.Lock()
	require.Len(t, repository.constraintCache.flights, maxCompiledConstraintFlights)
	require.Empty(t, repository.constraintCache.entries)
	repository.constraintCache.Unlock()
	for range 4 {
		go func() { _, err := repository.compileConstraint("1", source(0)); results <- err }()
	}
	unblock()
	for range 8 {
		require.NoError(t, <-results)
	}
	require.EqualValues(t, maxCompiledConstraintFlights, compilations.Load())
	require.Empty(t, repository.constraintCache.flights)
	t.Logf("unrelated cold sources simultaneously inside compiler: %d; four identical older-revision readers add zero compilations", maxCompiledConstraintFlights)
}

func TestMatcherCacheEvictionBoundsAndOldReferences(t *testing.T) {
	for _, pressure := range []string{"entries", "weight"} {
		t.Run(pressure, func(t *testing.T) {
			repository := &Repository{}
			original := `{"version":2,"regex":{"/x":"original"}}`
			held, err := repository.compileConstraint("1", original)
			require.NoError(t, err)
			count := maxCompiledConstraintCacheEntries
			if pressure == "weight" {
				count = 300
			}
			for index := range count {
				source := fmt.Sprintf(`{"equals":{"/x":%d}}`, index)
				if pressure == "weight" {
					source += strings.Repeat(" ", int(mustLimit("constraint_bytes"))-len(source))
				}
				_, err := repository.compileConstraint(fmt.Sprint(index+2), source)
				require.NoError(t, err)
				require.LessOrEqual(t, repository.constraintCache.weight, int64(maxCompiledConstraintCacheWeight))
				require.LessOrEqual(t, len(repository.constraintCache.entries), maxCompiledConstraintCacheEntries)
			}
			require.NotContains(t, repository.constraintCache.entries, original)
			require.True(t, held.atoms[0].expression.MatchString("original"))
			require.False(t, held.atoms[0].expression.MatchString("different"))
			recompiled, err := repository.compileConstraint("1", original)
			require.NoError(t, err)
			require.NotSame(t, held.atoms[0].expression, recompiled.atoms[0].expression)
			require.Equal(t, held.JSON(), recompiled.JSON())
			before := repository.constraintCache.weight
			_, err = repository.compileConstraint("99", strings.Repeat("x", int(mustLimit("constraint_bytes"))+1))
			require.ErrorIs(t, err, ErrInvalidInput)
			require.Equal(t, before, repository.constraintCache.weight)
			for range 2 {
				_, err = repository.compileConstraint("99", `{"version":2,"regex":{"/x":"["}}`)
				require.ErrorIs(t, err, ErrInvalidInput)
			}
			require.Empty(t, repository.constraintCache.flights)
		})
	}
}

func TestMatcherCacheConcurrentRevisionChurnAndEviction(t *testing.T) {
	repository := &Repository{}
	source := `{"version":2,"regex":{"/x":"old"}}`
	held, err := repository.compileConstraint("1", source)
	require.NoError(t, err)
	results := make(chan error, 4)
	for worker := range 4 {
		go func() {
			for index := range 1100 {
				compiled, err := repository.compileConstraint(fmt.Sprint(worker+index+2), fmt.Sprintf(`{"version":2,"regex":{"/x":"%d/%d"}}`, worker, index))
				if err != nil {
					results <- err
					return
				}
				if !compiled.atoms[0].expression.MatchString(fmt.Sprintf("%d/%d", worker, index)) || !held.atoms[0].expression.MatchString("old") {
					results <- fmt.Errorf("concurrent revision/eviction changed a program")
					return
				}
			}
			results <- nil
		}()
	}
	for range 4 {
		require.NoError(t, <-results)
	}
	require.Len(t, repository.constraintCache.entries, maxCompiledConstraintCacheEntries)
	require.NotContains(t, repository.constraintCache.entries, source)
	require.Empty(t, repository.constraintCache.flights)
	var weight int64
	for _, element := range repository.constraintCache.entries {
		weight += element.Value.(cachedConstraint).weight
	}
	require.Equal(t, weight, repository.constraintCache.weight)
	require.LessOrEqual(t, weight, int64(maxCompiledConstraintCacheWeight))
	require.True(t, held.atoms[0].expression.MatchString("old"))
}

func TestMatcherCacheOlderSnapshotAndMalformedReads(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, repository)
	subject := admitSelfProjectionSubject(t, repository, credential.Bearer)
	original := `{"version":2,"regex":{"/x":"old"}}`
	seedProjectedGrant(t, store, projectedGrantRow{id: id(1000), principalID: principal.ID, effect: contract.GrantAllow, serverID: id(51), upstreamName: "echo", constraint: original})
	request := EvaluationRequest{PrincipalID: principal.ID, ServerID: id(51), UpstreamName: "echo", Arguments: []byte(`{"x":"old"}`)}
	initial, err := repository.Evaluate(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, contract.DecisionAllow, initial.Decision)
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	results := make(chan contract.AuthorizationResult, 1)
	errors := make(chan error, 1)
	go func() {
		errors <- repository.view(context.Background(), func(tx *sql.Tx) error {
			if _, err := authorizationRevisionTx(context.Background(), tx); err != nil {
				return err
			}
			close(ready)
			<-release
			args, err := strictjson.ParseValue(request.Arguments, strictjson.Options{MaxBytes: 8192, MaxDepth: 64})
			if err != nil {
				return err
			}
			result, err := evaluateTx(repository, context.Background(), tx, principal.ID, id(51), "echo", args, testNow)
			results <- result
			return err
		})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not open")
	}
	require.NoError(t, store.Mutate(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `UPDATE grants SET constraint_json = ? WHERE id = ?`, `{"version":2,"regex":{"/x":"new"}}`, id(1000)); err != nil {
			return err
		}
		return advanceAuthorizationRevisionTx(context.Background(), tx)
	}))
	current, err := repository.Evaluate(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, contract.DecisionBlock, current.Decision)
	unblock()
	require.NoError(t, <-errors)
	old := <-results
	require.Equal(t, contract.DecisionAllow, old.Decision)
	require.Equal(t, initial.AuthorizationRevision, old.AuthorizationRevision)
	require.NotEqual(t, old.AuthorizationRevision, current.AuthorizationRevision)
	service, err := NewSelfProjectionService(repository, &fakeSelfGrantTargets{namespaces: map[string]string{contract.SyntheticServerID: contract.SyntheticServerNamespace, id(51): "sample"}})
	require.NoError(t, err)
	_, err = repository.ListGrants(context.Background(), GrantFilter{}, nil, 100)
	require.NoError(t, err)
	_, err = service.ListSelfGrants(context.Background(), subject, nil, 100)
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE grants SET constraint_json = ? WHERE id = ?`, `{"version":2,"regex":{"/x":"["}}`, id(1000))
		return err
	}))
	var group sync.WaitGroup
	for _, read := range []func(){
		func() {
			_, err := repository.Evaluate(context.Background(), request)
			require.ErrorIs(t, err, ErrAuthorizationUnavailable)
		},
		func() {
			_, err := repository.ListGrants(context.Background(), GrantFilter{}, nil, 100)
			require.ErrorIs(t, err, ErrInvalidState)
		},
		func() {
			_, err := service.ListSelfGrants(context.Background(), subject, nil, 100)
			require.ErrorIs(t, err, ErrInvalidState)
		},
	} {
		group.Add(1)
		go func() { defer group.Done(); read() }()
	}
	group.Wait()
}

func TestMatcherCacheReadMeasurements(t *testing.T) {
	for _, surface := range []string{"admin", "self"} {
		t.Run(surface, func(t *testing.T) {
			repository, store := newRepository(t, nil)
			principal, credential := createAdmissionCredential(t, repository)
			subject := admitSelfProjectionSubject(t, repository, credential.Bearer)
			page, err := repository.ListGrants(context.Background(), GrantFilter{PrincipalID: principal.ID}, nil, 100)
			require.NoError(t, err)
			for _, grant := range page.Items {
				require.NoError(t, repository.DeleteGrant(context.Background(), grant.ID))
			}
			for index := range 100 {
				seedProjectedGrant(t, store, projectedGrantRow{id: id(1000 + index), principalID: principal.ID, effect: contract.GrantAllow, serverID: id(51), upstreamName: "echo", constraint: fmt.Sprintf(`{"version":2,"regex":{"/resource":"item/%d/[0-9]+"}}`, index)})
			}
			service, err := NewSelfProjectionService(repository, &fakeSelfGrantTargets{namespaces: map[string]string{id(51): "sample"}})
			require.NoError(t, err)
			var compilations atomic.Int64
			repository.constraintCompiler = func(source []byte) (CompiledConstraint, error) {
				compilations.Add(1)
				return CompileConstraint(source)
			}
			for range 10 {
				if surface == "admin" {
					result, readErr := repository.ListGrants(context.Background(), GrantFilter{PrincipalID: principal.ID}, nil, 100)
					require.NoError(t, readErr)
					require.Len(t, result.Items, 100)
					require.Equal(t, `{"version":2,"regex":{"/resource":"item/0/[0-9]+"}}`, string(*result.Items[0].Constraint))
				} else {
					result, readErr := service.ListSelfGrants(context.Background(), subject, nil, 100)
					require.NoError(t, readErr)
					require.Len(t, result.Items, 100)
					require.Equal(t, `{"version":2,"regex":{"/resource":"item/0/[0-9]+"}}`, string(*result.Items[0].Policy.Constraint))
				}
			}
			t.Logf("100 distinct regex rows x 10 %s pages: %d compilations", surface, compilations.Load())
			require.EqualValues(t, 100, compilations.Load())
		})
	}
}

func TestMatcherCacheRevisionMeasurements(t *testing.T) {
	repository := &Repository{}
	var compilations atomic.Int64
	repository.constraintCompiler = func(source []byte) (CompiledConstraint, error) {
		compilations.Add(1)
		return CompileConstraint(source)
	}
	for revision := 1; revision <= 10; revision++ {
		for index := range 100 {
			_, err := repository.compileConstraint(fmt.Sprint(revision), fmt.Sprintf(`{"version":2,"regex":{"/resource":"item/%d/[0-9]+"}}`, index))
			require.NoError(t, err)
		}
	}
	t.Logf("100 distinct regex sources x 10 revisions: %d compilations", compilations.Load())
	require.EqualValues(t, 100, compilations.Load())
}
