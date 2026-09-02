package testutil

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const (
	testProcessGracePeriod = 100 * time.Millisecond
	testProcessKillPeriod  = 500 * time.Millisecond
)

var errProcessStopped = errors.New("test process stopped")

type ProcessCleanup struct {
	ProcessGroupID int
	TermSent       bool
	KillSent       bool
	Reaped         bool
	Survived       bool
}

type processExit struct {
	err error
}

type processSupervisor struct {
	command *exec.Cmd
	groupID int
	exited  <-chan processExit
}

func newProcessSupervisor(command *exec.Cmd, groupID int, exited <-chan processExit) *processSupervisor {
	return &processSupervisor{command: command, groupID: groupID, exited: exited}
}

func (supervisor *processSupervisor) stop() (ProcessCleanup, error, error) {
	cleanup := ProcessCleanup{ProcessGroupID: supervisor.groupID}
	if !revalidateTestProcessGroup(supervisor.command.Process, supervisor.groupID) {
		select {
		case exit := <-supervisor.exited:
			cleanup.Reaped = true
			cleanup.Survived = testProcessGroupAlive(supervisor.groupID)
			if cleanup.Survived {
				return cleanup, exit.err, fmt.Errorf("owned process group %d survived after its leader exited", supervisor.groupID)
			}
			return cleanup, exit.err, nil
		default:
			cleanup.Survived = true
			return cleanup, nil, fmt.Errorf("owned process group %d identity could not be revalidated", supervisor.groupID)
		}
	}

	cleanup.TermSent = signalTestProcessGroup(supervisor.groupID, syscall.SIGTERM) == nil
	if !cleanup.TermSent {
		cleanup.TermSent = supervisor.command.Process.Signal(syscall.SIGTERM) == nil
	}

	grace := time.NewTimer(testProcessGracePeriod)
	defer grace.Stop()
	var runErr error
	leaderReaped := false
	select {
	case exit := <-supervisor.exited:
		runErr, leaderReaped = exit.err, true
		<-grace.C
	case <-grace.C:
	}

	if testProcessGroupAlive(supervisor.groupID) {
		cleanup.KillSent = signalTestProcessGroup(supervisor.groupID, syscall.SIGKILL) == nil
		if !cleanup.KillSent {
			cleanup.KillSent = supervisor.command.Process.Kill() == nil
		}
	}

	deadline := time.NewTimer(testProcessKillPeriod)
	defer deadline.Stop()
	if !leaderReaped {
		select {
		case exit := <-supervisor.exited:
			runErr, leaderReaped = exit.err, true
		case <-deadline.C:
		}
	}
	cleanup.Reaped = leaderReaped

	for leaderReaped && testProcessGroupAlive(supervisor.groupID) {
		select {
		case <-deadline.C:
			cleanup.Survived = true
			return cleanup, runErr, fmt.Errorf("owned process group %d survived TERM/KILL cleanup", supervisor.groupID)
		case <-time.After(time.Millisecond):
		}
	}
	cleanup.Survived = !leaderReaped || testProcessGroupAlive(supervisor.groupID)
	if cleanup.Survived {
		return cleanup, runErr, fmt.Errorf("owned process group %d was not reaped", supervisor.groupID)
	}
	return cleanup, runErr, nil
}

func superviseProcess(ctx context.Context, supervisor *processSupervisor, stop <-chan struct{}) (ProcessCleanup, error) {
	select {
	case exit := <-supervisor.exited:
		cleanup := ProcessCleanup{ProcessGroupID: supervisor.groupID, Reaped: true}
		cleanup.Survived = testProcessGroupAlive(supervisor.groupID)
		if cleanup.Survived {
			return cleanup, errors.Join(exit.err, fmt.Errorf("owned process group %d survived command exit", supervisor.groupID))
		}
		return cleanup, exit.err
	case <-ctx.Done():
		cleanup, _, cleanupErr := supervisor.stop()
		return cleanup, errors.Join(ctx.Err(), cleanupErr)
	case <-stop:
		cleanup, _, cleanupErr := supervisor.stop()
		return cleanup, errors.Join(errProcessStopped, cleanupErr)
	}
}
