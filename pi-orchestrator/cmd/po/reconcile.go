package main

import (
	"context"
	"errors"
	"os"
	"syscall"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

var supervisorProcessAlive = defaultSupervisorProcessAlive

func reconcileWorkflowRun(ctx context.Context, db *store.Store, run store.WorkflowRun) (store.WorkflowRun, error) {
	if isTerminalState(run.State) || supervisorProcessAlive(run.SupervisorPID) {
		return run, nil
	}
	if err := db.UpdateWorkflowRunState(ctx, run.ID, store.StateUnknown, "workflow supervisor is not running", nowFunc().UTC()); err != nil {
		return store.WorkflowRun{}, err
	}
	run.State = store.StateUnknown
	run.Outcome = "workflow supervisor is not running"
	run.UpdatedAt = nowFunc().UTC()
	return run, nil
}

func defaultSupervisorProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	return true
}
