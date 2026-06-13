package supervisor

import (
	"context"

	pddispatcher "github.com/averycrespi/agent-tools/pi-dispatcher/pkg/dispatcher"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

type PDStartRequest = pddispatcher.StartTaskRunRequest
type PDStartResult = pddispatcher.StartTaskRunResult

type PDStarter interface {
	StartTaskRun(context.Context, PDStartRequest) (PDStartResult, error)
}

type DispatcherRunner struct {
	Starter PDStarter
}

func NewDispatcherRunner(starter PDStarter) DispatcherRunner {
	return DispatcherRunner{Starter: starter}
}

func (r DispatcherRunner) RunStep(ctx context.Context, req StepRequest) (StepResult, error) {
	result, err := r.Starter.StartTaskRun(ctx, pddispatcher.StartTaskRunRequest{
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
	return StepResult{PDTaskID: result.TaskID, PDRunID: result.RunID, State: store.StateSucceeded}, nil
}
