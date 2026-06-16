package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
)

type StepRequest struct {
	WorkflowRunID      string
	StepID             string
	Agent              workflow.Agent
	Prompt             string
	Repo               string
	Branch             string
	WorktreePath       string
	ArtifactParentPath string
}

type StepResult struct {
	PDTaskID     string
	PDRunID      string
	PDStdoutPath string
	PDStderrPath string
	PDEventsPath string
	State        store.State
	Outcome      string
}

type StepRunner interface {
	RunStep(context.Context, StepRequest) (StepResult, error)
}

type StepStarter interface {
	StartStep(context.Context, StepRequest) (StepHandle, error)
}

type StepHandle interface {
	Started() StepResult
	Wait(context.Context) (StepResult, error)
}

type StoppableStepHandle interface {
	StepHandle
	Stop(context.Context) error
}

type Logger struct {
	Writer io.Writer
}

func (l Logger) Printf(format string, args ...any) {
	if l.Writer == nil {
		return
	}
	_, _ = fmt.Fprintf(l.Writer, time.Now().UTC().Format(time.RFC3339)+" "+format+"\n", args...)
}

func Execute(ctx context.Context, db *store.Store, runner StepRunner, def *workflow.Definition, run store.WorkflowRun) error {
	return ExecuteWithLogger(ctx, db, runner, def, run, Logger{})
}

func ExecuteWithLogger(ctx context.Context, db *store.Store, runner StepRunner, def *workflow.Definition, run store.WorkflowRun, logger Logger) error {
	logger.Printf("workflow %s started workflow=%s steps=%d", run.ID, def.Name, len(def.Steps))
	if err := db.UpsertArtifacts(ctx, workflowArtifacts(run, def)); err != nil {
		return err
	}
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
				logger.Printf("step %s skipped reason=dependency_not_succeeded", step.ID)
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
			renderedPrompt, err := RenderPrompt(run, def, step, inputs)
			if err != nil {
				return err
			}
			request := StepRequest{WorkflowRunID: run.ID, StepID: step.ID, Agent: def.Agents[step.Agent], Prompt: renderedPrompt, Repo: run.Repo, Branch: run.Branch, WorktreePath: run.WorktreePath, ArtifactParentPath: filepath.Dir(run.ArtifactRoot)}
			logger.Printf("step %s starting agent=%s", step.ID, step.Agent)
			result, err := startAndPersistStep(ctx, db, runner, request, run, step, executionIndex)
			executionIndex++
			if err != nil {
				result.State = store.StateFailed
				result.Outcome = err.Error()
			}
			if result.State == "" {
				result.State = store.StateSucceeded
			}
			finalState := result.State
			outcome := result.Outcome
			if finalState == store.StateSucceeded {
				if failure, err := evaluateProducesChecks(ctx, db, run, def, step); err != nil {
					return err
				} else if failure != "" {
					finalState = store.StateFailed
					outcome = failure
				}
			}
			if err := db.UpdateStepState(ctx, run.ID, step.ID, finalState, outcome, time.Now().UTC()); err != nil {
				logger.Printf("step %s failed_to_record_state error=%q", step.ID, err.Error())
				return err
			}
			if outcome != "" {
				logger.Printf("step %s completed state=%s outcome=%q", step.ID, finalState, outcome)
			} else {
				logger.Printf("step %s completed state=%s", step.ID, finalState)
			}
			stepStates[step.ID] = finalState
			delete(remaining, step.ID)
			progressed = true
			if finalState != store.StateSucceeded && firstErr == nil {
				firstErr = fmt.Errorf("step %s failed: %s", step.ID, outcome)
			}
		}
		if !progressed {
			logger.Printf("workflow %s failed outcome=%q", run.ID, "no executable workflow steps remain")
			return fmt.Errorf("no executable workflow steps remain")
		}
	}
	terminal := store.StateSucceeded
	outcome := ""
	if firstErr != nil {
		terminal = store.StateFailed
		outcome = firstErr.Error()
		for _, state := range stepStates {
			if state == store.StateStopped {
				terminal = store.StateStopped
				break
			}
		}
	}
	if err := db.UpdateWorkflowRunState(ctx, run.ID, terminal, outcome, time.Now().UTC()); err != nil {
		logger.Printf("workflow %s failed_to_record_state error=%q", run.ID, err.Error())
		return err
	}
	if outcome != "" {
		logger.Printf("workflow %s completed state=%s outcome=%q", run.ID, terminal, outcome)
	} else {
		logger.Printf("workflow %s completed state=%s", run.ID, terminal)
	}
	return firstErr
}

func startAndPersistStep(ctx context.Context, db *store.Store, runner StepRunner, request StepRequest, run store.WorkflowRun, step workflow.Step, executionIndex int) (StepResult, error) {
	if starter, ok := runner.(StepStarter); ok {
		handle, err := starter.StartStep(ctx, request)
		result := StepResult{State: store.StateFailed}
		if handle != nil {
			result = handle.Started()
		}
		if createErr := createRunningStep(ctx, db, run, step, executionIndex, result); createErr != nil {
			if stopper, ok := handle.(StoppableStepHandle); ok {
				_ = stopper.Stop(ctx)
			}
			return StepResult{}, createErr
		}
		if err != nil {
			return result, err
		}
		return handle.Wait(ctx)
	}
	result, err := runner.RunStep(ctx, request)
	if createErr := createRunningStep(ctx, db, run, step, executionIndex, result); createErr != nil {
		return StepResult{}, createErr
	}
	return result, err
}

func createRunningStep(ctx context.Context, db *store.Store, run store.WorkflowRun, step workflow.Step, executionIndex int, result StepResult) error {
	now := time.Now().UTC()
	stepRun := store.StepRun{WorkflowRunID: run.ID, StepID: step.ID, Agent: step.Agent, ExecutionIndex: executionIndex, State: store.StateRunning, PDTaskID: result.PDTaskID, PDRunID: result.PDRunID, PDStdoutPath: result.PDStdoutPath, PDStderrPath: result.PDStderrPath, PDEventsPath: result.PDEventsPath, StartedAt: now, UpdatedAt: now}
	return db.CreateStepRun(ctx, stepRun)
}

func RenderPrompt(run store.WorkflowRun, def *workflow.Definition, step workflow.Step, inputs map[string]any) (string, error) {
	artifactPaths := artifactPathsForWorkflow(run, def)
	tmpl, err := template.New("step-prompt").Option("missingkey=error").Parse(step.Prompt)
	if err != nil {
		return "", fmt.Errorf("parse prompt for step %s: %w", step.ID, err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, map[string]any{"Inputs": promptSafeInputs(inputs), "Artifacts": artifactPaths}); err != nil {
		return "", fmt.Errorf("render prompt for step %s: %w", step.ID, err)
	}
	return rendered.String(), nil
}

type promptSafeString struct {
	Name  string
	Value string
}

func (s promptSafeString) String() string {
	encoded, err := json.Marshal(s.Value)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%q", s.Value))
	}
	return fmt.Sprintf("<untrusted-workflow-input name=%q>%s</untrusted-workflow-input>", s.Name, string(encoded))
}

func promptSafeInputs(inputs map[string]any) map[string]any {
	safe := make(map[string]any, len(inputs))
	for name, value := range inputs {
		if text, ok := value.(string); ok {
			safe[name] = promptSafeString{Name: name, Value: text}
			continue
		}
		safe[name] = value
	}
	return safe
}

func artifactPathsForWorkflow(run store.WorkflowRun, def *workflow.Definition) map[string]string {
	artifactPaths := make(map[string]string, len(def.Artifacts))
	for name, artifact := range def.Artifacts {
		artifactPaths[name] = filepath.Join(run.ArtifactRoot, artifact.Path)
	}
	return artifactPaths
}

func workflowArtifacts(run store.WorkflowRun, def *workflow.Definition) []store.Artifact {
	artifacts := make([]store.Artifact, 0, len(def.Artifacts))
	now := time.Now().UTC()
	for name, artifact := range def.Artifacts {
		absolutePath := filepath.Join(run.ArtifactRoot, artifact.Path)
		info, err := statArtifactPath(run.ArtifactRoot, artifact.Path)
		artifacts = append(artifacts, store.Artifact{WorkflowRunID: run.ID, Name: name, RelativePath: artifact.Path, AbsolutePath: absolutePath, Exists: err == nil && info.Mode().IsRegular(), UpdatedAt: now})
	}
	return artifacts
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
	return db.CreateStepRun(ctx, stepRun)
}

func evaluateProducesChecks(ctx context.Context, db *store.Store, run store.WorkflowRun, def *workflow.Definition, step workflow.Step) (string, error) {
	if len(step.Produces) == 0 {
		return "", nil
	}
	checked := make([]store.Artifact, 0, len(step.Produces))
	checkResults := make([]store.StepCheckResult, 0, len(step.Produces))
	names := make([]string, 0, len(step.Produces))
	for name := range step.Produces {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		check := step.Produces[name]
		artifact := def.Artifacts[name]
		absolutePath := filepath.Join(run.ArtifactRoot, artifact.Path)
		info, err := statArtifactPath(run.ArtifactRoot, artifact.Path)
		exists := err == nil && info.Mode().IsRegular()
		checked = append(checked, store.Artifact{WorkflowRunID: run.ID, Name: name, RelativePath: artifact.Path, AbsolutePath: absolutePath, Exists: exists, UpdatedAt: time.Now().UTC()})
		if !exists {
			message := fmt.Sprintf("artifact %s failed %s check: %s is not a regular file", name, check, artifact.Path)
			if err != nil && os.IsNotExist(err) {
				message = fmt.Sprintf("artifact %s failed %s check: %s is missing", name, check, artifact.Path)
			}
			checkResults = append(checkResults, store.StepCheckResult{WorkflowRunID: run.ID, StepID: step.ID, Kind: "artifact", Target: name, Check: string(check), Passed: false, Message: message, UpdatedAt: time.Now().UTC()})
			if updateErr := db.UpsertArtifacts(ctx, checked); updateErr != nil {
				return "", updateErr
			}
			if updateErr := db.UpsertStepCheckResults(ctx, checkResults); updateErr != nil {
				return "", updateErr
			}
			return message, nil
		}
		if check == workflow.ProduceNonEmpty && info.Size() == 0 {
			message := fmt.Sprintf("artifact %s failed %s check: %s is empty", name, check, artifact.Path)
			checkResults = append(checkResults, store.StepCheckResult{WorkflowRunID: run.ID, StepID: step.ID, Kind: "artifact", Target: name, Check: string(check), Passed: false, Message: message, UpdatedAt: time.Now().UTC()})
			if updateErr := db.UpsertArtifacts(ctx, checked); updateErr != nil {
				return "", updateErr
			}
			if updateErr := db.UpsertStepCheckResults(ctx, checkResults); updateErr != nil {
				return "", updateErr
			}
			return message, nil
		}
		checkResults = append(checkResults, store.StepCheckResult{WorkflowRunID: run.ID, StepID: step.ID, Kind: "artifact", Target: name, Check: string(check), Passed: true, Message: "", UpdatedAt: time.Now().UTC()})
	}
	if err := db.UpsertArtifacts(ctx, checked); err != nil {
		return "", err
	}
	if err := db.UpsertStepCheckResults(ctx, checkResults); err != nil {
		return "", err
	}
	return "", nil
}

func statArtifactPath(root string, relativePath string) (os.FileInfo, error) {
	cleaned := filepath.Clean(relativePath)
	current := root
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is not a regular file", relativePath)
		}
	}
	return os.Lstat(filepath.Join(root, cleaned))
}
