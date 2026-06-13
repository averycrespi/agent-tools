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
	result, err := r.Client.StartTaskRun(ctx, pddispatcher.StartTaskRunRequest{
		RepoPath:     req.Repo,
		RepoName:     "",
		Branch:       req.Branch,
		WorktreePath: req.WorktreePath,
		Prompt:       req.Prompt,
		PromptSource: "workflow:" + req.WorkflowRunID + ":" + req.StepID,
		Agent: pddispatcher.AgentOptions{
			Model:  req.Agent.Model,
			Skills: req.Agent.Skills,
		},
	})
	if err != nil {
		return StepResult{State: store.StateFailed, Outcome: err.Error()}, err
	}
	info, err := r.Client.WaitTaskRun(ctx, pddispatcher.WaitTaskRunRequest{TaskID: result.TaskID, RunID: result.RunID})
	if err != nil {
		return StepResult{PDTaskID: result.TaskID, PDRunID: result.RunID, State: store.StateFailed, Outcome: err.Error()}, err
	}
	return StepResult{PDTaskID: result.TaskID, PDRunID: result.RunID, State: mapPDStatus(info.Status), Outcome: info.ErrorMessage}, nil
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
