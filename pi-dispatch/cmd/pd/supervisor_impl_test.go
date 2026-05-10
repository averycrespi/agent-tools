package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSupervisorFinalStatusUsesStoppedAfterStopRequest(t *testing.T) {
	state := &supervisorRunState{stopRequested: true}
	require.Equal(t, store.StatusStopped, state.finalStatus(nil))
	require.Equal(t, store.StatusStopped, state.finalStatus(errors.New("signal: killed")))
}

func TestSupervisorFinalStatusUsesSucceededOnlyWithoutStopRequest(t *testing.T) {
	state := &supervisorRunState{}
	require.Equal(t, store.StatusSucceeded, state.finalStatus(nil))
	require.Equal(t, store.StatusFailed, state.finalStatus(errors.New("boom")))
}

func TestApplyStopRequestAbortsAndSchedulesForcedKill(t *testing.T) {
	state := &supervisorRunState{}
	client := &fakeAbortClient{}
	proc := &fakeKillProcess{wait: make(chan struct{})}

	resp := applyStopRequest(state, client, proc, control.Request{Operation: control.OpStop, Force: true}, 0)

	require.True(t, resp.OK)
	require.True(t, state.stopRequested)
	require.True(t, state.forceRequested)
	require.True(t, client.aborted)
	require.True(t, proc.wasKilled())
}

func TestApplyStopRequestDoesNotMarkStoppedWhenAbortFails(t *testing.T) {
	state := &supervisorRunState{}
	client := &fakeAbortClient{err: errors.New("abort failed")}
	proc := &fakeKillProcess{wait: make(chan struct{})}

	resp := applyStopRequest(state, client, proc, control.Request{Operation: control.OpStop, Force: true}, 0)

	require.False(t, resp.OK)
	require.False(t, state.stopRequested)
	require.False(t, state.forceRequested)
	require.True(t, client.aborted)
	require.False(t, proc.wasKilled())
	require.Equal(t, store.StatusSucceeded, state.finalStatus(nil))
}

func TestScheduleForceKillWaitsForGracePeriod(t *testing.T) {
	proc := &fakeKillProcess{wait: make(chan struct{})}
	scheduleForceKill(proc, 30*time.Millisecond)
	require.False(t, proc.wasKilled())
	require.Eventually(t, proc.wasKilled, time.Second, 10*time.Millisecond)
}

type fakeAbortClient struct {
	aborted bool
	err     error
}

func (c *fakeAbortClient) Abort() error {
	c.aborted = true
	return c.err
}

type fakeKillProcess struct {
	killed atomic.Bool
	wait   chan struct{}
}

func (p *fakeKillProcess) Kill() error {
	p.killed.Store(true)
	return nil
}

func (p *fakeKillProcess) wasKilled() bool { return p.killed.Load() }

func TestSupervisorRunStateConcurrentStopAndFinalStatus(t *testing.T) {
	state := &supervisorRunState{}
	client := &fakeAbortClient{}
	proc := &fakeKillProcess{wait: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			_ = state.finalStatus(nil)
		}
	}()
	for range 1000 {
		_ = applyStopRequest(state, client, proc, control.Request{Operation: control.OpStop}, time.Hour)
	}
	<-done
}
