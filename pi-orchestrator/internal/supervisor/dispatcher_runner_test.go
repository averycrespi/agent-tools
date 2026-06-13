package supervisor

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
)

func TestDispatcherRunnerStartsPDRunOnWorkflowWorktree(t *testing.T) {
	t.Parallel()
	starter := &recordingPDStarter{result: PDStartResult{TaskID: "pd-task-1", RunID: "pd-run-1"}}
	runner := DispatcherRunner{Starter: starter}

	result, err := runner.RunStep(context.Background(), StepRequest{
		WorkflowRunID: "po-run-1",
		StepID:        "review",
		Agent: workflow.Agent{
			Model:  "gpt-5.1-codex",
			Skills: []string{"review"},
		},
		Prompt:       "rendered prompt",
		Repo:         "/repo",
		Branch:       "po/review-abcd",
		WorktreePath: "/worktrees/review-abcd",
	})
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if result.PDTaskID != "pd-task-1" || result.PDRunID != "pd-run-1" || result.State != store.StateSucceeded {
		t.Fatalf("result = %+v, want backing pd ids and succeeded admission", result)
	}
	if starter.request.RepoPath != "/repo" || starter.request.Branch != "po/review-abcd" || starter.request.WorktreePath != "/worktrees/review-abcd" {
		t.Fatalf("request = %+v, want shared workflow repo/branch/worktree", starter.request)
	}
	if starter.request.Prompt != "rendered prompt" || starter.request.PromptSource != "workflow:po-run-1:review" {
		t.Fatalf("request = %+v, want rendered workflow prompt source", starter.request)
	}
	if starter.request.Agent.Model != "gpt-5.1-codex" || len(starter.request.Agent.Skills) != 1 || starter.request.Agent.Skills[0] != "review" {
		t.Fatalf("agent = %+v, want model and skills pass-through", starter.request.Agent)
	}
}

type recordingPDStarter struct {
	request PDStartRequest
	result  PDStartResult
}

func (r *recordingPDStarter) StartTaskRun(_ context.Context, req PDStartRequest) (PDStartResult, error) {
	r.request = req
	return r.result, nil
}
