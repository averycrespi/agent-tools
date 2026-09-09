package invocation

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCompletionWriterAllowsConcurrentAdmissionAfterAcknowledgment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var armed atomic.Bool
	fault := func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit && armed.CompareAndSwap(true, false) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	_, audits, authority, _, credential := newAdmissionCoordinator(t, fault)
	var executions atomic.Int32
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				if executions.Add(1) == 1 {
					armed.Store(true)
				}
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	firstLease, err := authority.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	defer firstLease.Release()
	first := make(chan CallResponse, 1)
	go func() { first <- service.Call(ctx, firstLease, validCallParams()) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("completion did not reach cleanup barrier")
	}
	record := onlyInvocationRecord(t, audits)
	require.NotNil(t, record.TerminalClass)
	require.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
	secondLease, err := authority.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	defer secondLease.Release()
	second := make(chan CallResponse, 1)
	go func() { second <- service.Call(ctx, secondLease, validCallParams()) }()
	require.Eventually(t, func() bool { _, waiting := audits.store.MutationOccupancy(); return waiting == 1 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, executions.Load(), "no dispatch before audit acknowledgment")
	count, err := audits.Count(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	unblock()
	for _, done := range []chan CallResponse{first, second} {
		select {
		case response := <-done:
			require.NotNil(t, response.Result)
			require.Empty(t, response.ErrorCode)
		case <-ctx.Done():
			t.Fatal("call failed to settle")
		}
	}
	require.EqualValues(t, 2, executions.Load())
	count, err = audits.Count(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	require.False(t, audits.store.Latched())
	owned, waiting := audits.store.MutationOccupancy()
	require.False(t, owned)
	require.Zero(t, waiting)
}

func TestConcurrentCompletionsRetainBothTerminalOutcomes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var armed atomic.Bool
	_, audits, authority, _, credential := newAdmissionCoordinator(t, func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit && armed.CompareAndSwap(true, false) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	executing := make(chan struct{}, 2)
	finish := make(chan struct{})
	var finishOnce sync.Once
	finishCalls := func() { finishOnce.Do(func() { close(finish) }) }
	defer finishCalls()
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				executing <- struct{}{}
				select {
				case <-finish:
				case <-ctx.Done():
				}
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	done := make(chan CallResponse, 2)
	for range 2 {
		lease, err := authority.Authenticate(ctx, credential.Bearer)
		require.NoError(t, err)
		defer lease.Release()
		go func() { done <- service.Call(ctx, lease, validCallParams()) }()
	}
	for range 2 {
		select {
		case <-executing:
		case <-ctx.Done():
			t.Fatal("downstream calls did not overlap")
		}
	}
	page, err := audits.List(ctx, contract.InvocationListQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	armed.Store(true)
	finishCalls()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("completion did not reach barrier")
	}
	require.Eventually(t, func() bool { _, waiting := audits.store.MutationOccupancy(); return waiting == 1 }, time.Second, time.Millisecond)
	unblock()
	for range 2 {
		require.NotNil(t, (<-done).Result)
	}
	for _, item := range page.Items {
		record, found, err := audits.Read(ctx, item.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.NotNil(t, record.TerminalClass)
		require.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
	}
	require.False(t, audits.store.Latched())
}
