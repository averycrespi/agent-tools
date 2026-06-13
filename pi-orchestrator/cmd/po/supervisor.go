package main

import (
	"fmt"

	pddispatcher "github.com/averycrespi/agent-tools/pi-dispatcher/pkg/dispatcher"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	posupervisor "github.com/averycrespi/agent-tools/pi-orchestrator/internal/supervisor"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
	"github.com/spf13/cobra"
)

var supervisorRunID string

var defaultNewStepRunner = func() posupervisor.StepRunner {
	return posupervisor.NewDispatcherRunner(pddispatcher.NewClient(pddispatcher.Config{}))
}
var newStepRunner = defaultNewStepRunner

var supervisorCmd = &cobra.Command{
	Use:    "supervisor",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runSupervisorCommand,
}

func init() {
	supervisorCmd.Flags().StringVar(&supervisorRunID, "run-id", "", "workflow run id")
}

func runSupervisorCommand(cmd *cobra.Command, _ []string) error {
	if supervisorRunID == "" {
		return fmt.Errorf("--run-id is required")
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	run, err := db.GetWorkflowRun(cmd.Context(), supervisorRunID)
	if err != nil {
		return err
	}
	definition, err := workflowDefinitionForRun(run)
	if err != nil {
		_ = db.UpdateWorkflowRunState(cmd.Context(), run.ID, store.StateFailed, err.Error(), nowFunc().UTC())
		return err
	}
	if err := db.UpdateWorkflowRunState(cmd.Context(), run.ID, store.StateRunning, "", nowFunc().UTC()); err != nil {
		return err
	}
	run.State = store.StateRunning
	if err := posupervisor.Execute(cmd.Context(), db, newStepRunner(), definition, run); err != nil {
		return err
	}
	return nil
}

func workflowDefinitionForRun(run store.WorkflowRun) (*workflow.Definition, error) {
	if run.DefinitionYAML != "" {
		return workflow.LoadBytes([]byte(run.DefinitionYAML), run.Workflow, "workflow snapshot "+run.ID)
	}
	definitionPath, err := workflowFilePath(run.Workflow)
	if err != nil {
		return nil, err
	}
	return workflow.LoadFile(definitionPath)
}
