package supervisor

import (
	"context"

	pddispatcher "github.com/averycrespi/agent-tools/pi-dispatcher/pkg/dispatcher"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

type PDStartRequest = pddispatcher.StartTaskRunRequest
type PDStartResult = pddispatcher.StartTaskRunResult
type PDWaitRequest = pddispatcher.WaitTaskRunRequest
type PDTaskRunInfo = pddispatcher.TaskRunInfo

type PDClient interface {
	StartTaskRun(context.Context, PDStartRequest) (PDStartResult, error)
	WaitTaskRun(context.Context, PDWaitRequest) (PDTaskRunInfo, error)
}

type DispatcherRunner struct {
	Client PDClient
}

func NewDispatcherRunner(client PDClient) DispatcherRunner {
	return DispatcherRunner{Client: client}
}

func (r DispatcherRunner) RunStep(ctx context.Context, req StepRequest) (StepResult, error) {
	handle, err := r.StartStep(ctx, req)
	if err != nil {
		return StepResult{State: store.StateFailed, Outcome: err.Error()}, err
	}
	return handle.Wait(ctx)
}

func (r DispatcherRunner) StartStep(ctx context.Context, req StepRequest) (StepHandle, error) {
	result, err := r.Client.StartTaskRun(ctx, pddispatcher.StartTaskRunRequest{
		RepoPath:           req.Repo,
		RepoName:           "",
		Branch:             req.Branch,
		WorktreePath:       req.WorktreePath,
		ArtifactParentPath: req.ArtifactParentPath,
		Prompt:             req.Prompt,
		PromptSource:       "workflow:" + req.WorkflowRunID + ":" + req.StepID,
		Agent: pddispatcher.AgentOptions{
			Model:  req.Agent.Model,
			Skills: req.Agent.Skills,
		},
	})
	if err != nil {
		return nil, err
	}
	return dispatcherStepHandle{client: r.Client, started: StepResult{PDTaskID: result.TaskID, PDRunID: result.RunID, PDStdoutPath: result.StdoutPath, PDStderrPath: result.StderrPath, PDEventsPath: result.PiEventsPath, State: store.StateRunning}}, nil
}

type dispatcherStepHandle struct {
	client  PDClient
	started StepResult
}

func (h dispatcherStepHandle) Started() StepResult { return h.started }

func (h dispatcherStepHandle) Wait(ctx context.Context) (StepResult, error) {
	info, err := h.client.WaitTaskRun(ctx, pddispatcher.WaitTaskRunRequest{TaskID: h.started.PDTaskID, RunID: h.started.PDRunID})
	if err != nil {
		return StepResult{PDTaskID: h.started.PDTaskID, PDRunID: h.started.PDRunID, PDStdoutPath: h.started.PDStdoutPath, PDStderrPath: h.started.PDStderrPath, PDEventsPath: h.started.PDEventsPath, State: store.StateFailed, Outcome: err.Error()}, err
	}
	return StepResult{PDTaskID: h.started.PDTaskID, PDRunID: h.started.PDRunID, PDStdoutPath: h.started.PDStdoutPath, PDStderrPath: h.started.PDStderrPath, PDEventsPath: h.started.PDEventsPath, State: mapPDStatus(info.Status), Outcome: info.ErrorMessage}, nil
}

func mapPDStatus(status pddispatcher.TaskRunStatus) store.State {
	switch status {
	case pddispatcher.TaskRunStatusSucceeded:
		return store.StateSucceeded
	case pddispatcher.TaskRunStatusStopped:
		return store.StateStopped
	case pddispatcher.TaskRunStatusUnknown:
		return store.StateUnknown
	default:
		return store.StateFailed
	}
}
