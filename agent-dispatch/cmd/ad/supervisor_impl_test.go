package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
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

func TestScheduleForceKillWaitsForGracePeriod(t *testing.T) {
	proc := &fakeKillProcess{wait: make(chan struct{})}
	scheduleForceKill(proc, 30*time.Millisecond)
	require.False(t, proc.wasKilled())
	require.Eventually(t, proc.wasKilled, time.Second, 10*time.Millisecond)
}

type fakeAbortClient struct{ aborted bool }

func (c *fakeAbortClient) Abort() error {
	c.aborted = true
	return nil
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
