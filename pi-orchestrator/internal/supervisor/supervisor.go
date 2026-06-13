package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
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
	inputs := map[string]any{}
	if run.InputsJSON != "" {
		if err := json.Unmarshal([]byte(run.InputsJSON), &inputs); err != nil {
			return fmt.Errorf("decode workflow inputs: %w", err)
		}
	}
	remaining := make(map[string]workflow.Step, len(def.Steps))
	for _, step := range def.Steps {
		remaining[step.ID] = step
	}
	var firstErr error
	executionIndex := 0
	for len(remaining) > 0 {
		progressed := false
		for _, step := range def.Steps {
			if _, exists := remaining[step.ID]; !exists {
				continue
			}
			if hasFailedDependency(step, stepStates) {
				if err := createSkippedStep(ctx, db, run, step, executionIndex); err != nil {
					return err
				}
				executionIndex++
				stepStates[step.ID] = store.StateSkipped
				delete(remaining, step.ID)
				progressed = true
				continue
			}
			if !dependenciesSucceeded(step, stepStates) {
				continue
			}
			renderedPrompt, err := renderPrompt(run, step, inputs)
			if err != nil {
				return err
			}
			result, err := runner.RunStep(ctx, StepRequest{WorkflowRunID: run.ID, StepID: step.ID, Agent: def.Agents[step.Agent], Prompt: renderedPrompt, Repo: run.Repo, Branch: run.Branch, WorktreePath: run.WorktreePath})
			artifacts := artifactsForStep(run, step)
			if err != nil {
				result.State = store.StateFailed
				result.Outcome = err.Error()
			}
			if result.State == "" {
				result.State = store.StateSucceeded
			}
			now := time.Now().UTC()
			stepRun := store.StepRun{WorkflowRunID: run.ID, StepID: step.ID, Agent: step.Agent, ExecutionIndex: executionIndex, State: store.StateRunning, PDTaskID: result.PDTaskID, PDRunID: result.PDRunID, StartedAt: now, UpdatedAt: now}
			executionIndex++
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
			delete(remaining, step.ID)
			progressed = true
			if finalState != store.StateSucceeded {
				firstErr = fmt.Errorf("step %s failed: %s", step.ID, outcome)
			}
		}
		if !progressed {
			return fmt.Errorf("no executable workflow steps remain")
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

func renderPrompt(run store.WorkflowRun, step workflow.Step, inputs map[string]any) (string, error) {
	artifactPaths := make(map[string]string, len(step.Artifacts))
	for _, artifact := range step.Artifacts {
		artifactPaths[artifact.Name] = filepath.Join(run.ArtifactRoot, artifact.Path)
	}
	funcs := template.FuncMap{
		"artifact_path": func(name string) (string, error) {
			path, ok := artifactPaths[name]
			if !ok {
				return "", fmt.Errorf("unknown artifact %s", name)
			}
			return path, nil
		},
	}
	tmpl, err := template.New("step-prompt").Funcs(funcs).Parse(step.Prompt)
	if err != nil {
		return "", fmt.Errorf("parse prompt for step %s: %w", step.ID, err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, map[string]any{"Inputs": inputs}); err != nil {
		return "", fmt.Errorf("render prompt for step %s: %w", step.ID, err)
	}
	return rendered.String(), nil
}

func hasFailedDependency(step workflow.Step, stepStates map[string]store.State) bool {
	for _, need := range step.Needs {
		state, exists := stepStates[need]
		if exists && state != store.StateSucceeded {
			return true
		}
	}
	return false
}

func dependenciesSucceeded(step workflow.Step, stepStates map[string]store.State) bool {
	for _, need := range step.Needs {
		if stepStates[need] != store.StateSucceeded {
			return false
		}
	}
	return true
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
