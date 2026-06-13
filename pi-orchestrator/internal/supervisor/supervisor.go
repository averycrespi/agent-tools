package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
)

type StepRequest struct {
	WorkflowRunID string
	StepID        string
	Agent         workflow.Agent
	Prompt        string
	Repo          string
	Branch        string
	WorktreePath  string
}

type StepResult struct {
	PDTaskID string
	PDRunID  string
	State    store.State
	Outcome  string
}

type StepRunner interface {
	RunStep(context.Context, StepRequest) (StepResult, error)
}

func Execute(ctx context.Context, db *store.Store, runner StepRunner, def *workflow.Definition, run store.WorkflowRun) error {
	stepStates := make(map[string]store.State, len(def.Steps))
	var firstErr error
	for index, step := range def.Steps {
		if firstErr != nil || hasFailedDependency(step, stepStates) {
			if err := createSkippedStep(ctx, db, run, step, index); err != nil {
				return err
			}
			stepStates[step.ID] = store.StateSkipped
			continue
		}
		result, err := runner.RunStep(ctx, StepRequest{WorkflowRunID: run.ID, StepID: step.ID, Agent: def.Agents[step.Agent], Prompt: step.Prompt, Repo: run.Repo, Branch: run.Branch, WorktreePath: run.WorktreePath})
		artifacts := artifactsForStep(run, step)
		if err != nil {
			result.State = store.StateFailed
			result.Outcome = err.Error()
		}
		if result.State == "" {
			result.State = store.StateSucceeded
		}
		now := time.Now().UTC()
		stepRun := store.StepRun{WorkflowRunID: run.ID, StepID: step.ID, Agent: step.Agent, ExecutionIndex: index, State: store.StateRunning, PDTaskID: result.PDTaskID, PDRunID: result.PDRunID, StartedAt: now, UpdatedAt: now}
		if err := db.CreateStepRun(ctx, stepRun, artifacts); err != nil {
			return err
		}
		finalState := result.State
		outcome := result.Outcome
		if finalState == store.StateSucceeded {
			missing := missingRequiredArtifacts(artifacts)
			if len(missing) > 0 {
				finalState = store.StateFailed
				outcome = fmt.Sprintf("missing required artifact %s", missing[0].Name)
			}
		}
		if err := db.UpdateStepState(ctx, run.ID, step.ID, finalState, outcome, now); err != nil {
			return err
		}
		stepStates[step.ID] = finalState
		if finalState != store.StateSucceeded {
			firstErr = fmt.Errorf("step %s failed: %s", step.ID, outcome)
		}
	}
	terminal := store.StateSucceeded
	outcome := ""
	if firstErr != nil {
		terminal = store.StateFailed
		outcome = firstErr.Error()
	}
	if err := db.UpdateWorkflowRunState(ctx, run.ID, terminal, outcome, time.Now().UTC()); err != nil {
		return err
	}
	return firstErr
}

func hasFailedDependency(step workflow.Step, stepStates map[string]store.State) bool {
	for _, need := range step.Needs {
		if stepStates[need] != store.StateSucceeded {
			return true
		}
	}
	return false
}

func createSkippedStep(ctx context.Context, db *store.Store, run store.WorkflowRun, step workflow.Step, index int) error {
	now := time.Now().UTC()
	stepRun := store.StepRun{WorkflowRunID: run.ID, StepID: step.ID, Agent: step.Agent, ExecutionIndex: index, State: store.StateSkipped, Outcome: "dependency did not succeed", StartedAt: now, UpdatedAt: now}
	return db.CreateStepRun(ctx, stepRun, artifactsForStep(run, step))
}

func artifactsForStep(run store.WorkflowRun, step workflow.Step) []store.Artifact {
	artifacts := make([]store.Artifact, 0, len(step.Artifacts))
	for _, artifact := range step.Artifacts {
		absolutePath := filepath.Join(run.ArtifactRoot, artifact.Path)
		_, err := os.Stat(absolutePath)
		artifacts = append(artifacts, store.Artifact{WorkflowRunID: run.ID, StepID: step.ID, Name: artifact.Name, RelativePath: artifact.Path, AbsolutePath: absolutePath, Required: artifact.Required, Exists: err == nil, UpdatedAt: time.Now().UTC()})
	}
	return artifacts
}

func missingRequiredArtifacts(artifacts []store.Artifact) []store.Artifact {
	var missing []store.Artifact
	for _, artifact := range artifacts {
		if artifact.Required && !artifact.Exists {
			missing = append(missing, artifact)
		}
	}
	return missing
}
