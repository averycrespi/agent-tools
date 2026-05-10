package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
	"github.com/spf13/cobra"
)

var (
	supervisorTaskID string
	supervisorPiArgv string
)

func init() {
	supervisorCmd.Flags().StringVar(&supervisorTaskID, "task-id", "", "task ID")
	supervisorCmd.Flags().StringVar(&supervisorPiArgv, "pi-argv", "", "NUL-separated Pi argv")
	supervisorCmd.RunE = runSupervisor
}

func runSupervisor(cmd *cobra.Command, _ []string) error {
	if supervisorTaskID == "" {
		return fmt.Errorf("--task-id is required")
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	run, err := db.LatestRun(cmd.Context(), supervisorTaskID)
	if err != nil {
		return err
	}
	if err := db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusRunning); err != nil {
		return err
	}
	_ = db.AddEvent(cmd.Context(), store.Event{TaskID: supervisorTaskID, RunID: run.ID, Timestamp: time.Now(), Type: "supervisor.started", Message: "supervisor started"})
	argv := splitNUL(supervisorPiArgv)
	_ = db.AddEvent(cmd.Context(), store.Event{TaskID: supervisorTaskID, RunID: run.ID, Timestamp: time.Now(), Type: "supervisor.pi_argv", Message: strings.Join(argv, " ")})
	_ = db.AddEvent(cmd.Context(), store.Event{TaskID: supervisorTaskID, RunID: run.ID, Timestamp: time.Now(), Type: "supervisor.failed", Message: "Pi RPC supervision is not implemented yet"})
	return db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusFailed)
}

func splitNUL(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}
