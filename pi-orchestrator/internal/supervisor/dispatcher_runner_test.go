package supervisor

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
)

func TestDispatcherRunnerStartsPDRunOnWorkflowWorktree(t *testing.T) {
	t.Parallel()
	starter := &recordingPDClient{startResult: PDStartResult{TaskID: "pd-task-1", RunID: "pd-run-1"}, waitResult: PDTaskRunInfo{TaskID: "pd-task-1", RunID: "pd-run-1", Status: "succeeded"}}
	runner := DispatcherRunner{Client: starter}

	result, err := runner.RunStep(context.Background(), StepRequest{
		WorkflowRunID: "po-run-1",
		StepID:        "review",
		Agent: workflow.Agent{
			Model:  "gpt-5.1-codex",
			Skills: []string{"review"},
		},
		Prompt:             "rendered prompt",
		Repo:               "/repo",
		Branch:             "po/review-abcd",
		WorktreePath:       "/worktrees/review-abcd",
		ArtifactParentPath: "/artifacts",
	})
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if result.PDTaskID != "pd-task-1" || result.PDRunID != "pd-run-1" || result.State != store.StateSucceeded {
		t.Fatalf("result = %+v, want backing pd ids after succeeded run", result)
	}
	if starter.waitRequest.TaskID != "pd-task-1" || starter.waitRequest.RunID != "pd-run-1" {
		t.Fatalf("wait request = %+v, want backing pd ids", starter.waitRequest)
	}
	if starter.request.RepoPath != "/repo" || starter.request.Branch != "po/review-abcd" || starter.request.WorktreePath != "/worktrees/review-abcd" || starter.request.ArtifactParentPath != "/artifacts" {
		t.Fatalf("request = %+v, want shared workflow repo/branch/worktree and artifact parent", starter.request)
	}
	if starter.request.Prompt != "rendered prompt" || starter.request.PromptSource != "workflow:po-run-1:review" {
		t.Fatalf("request = %+v, want rendered workflow prompt source", starter.request)
	}
	if starter.request.Agent.Model != "gpt-5.1-codex" || len(starter.request.Agent.Skills) != 1 || starter.request.Agent.Skills[0] != "review" {
		t.Fatalf("agent = %+v, want model and skills pass-through", starter.request.Agent)
	}
}

func TestDispatcherRunnerMapsFailedPDRun(t *testing.T) {
	t.Parallel()
	client := &recordingPDClient{startResult: PDStartResult{TaskID: "pd-task-1", RunID: "pd-run-1"}, waitResult: PDTaskRunInfo{TaskID: "pd-task-1", RunID: "pd-run-1", Status: "failed", ErrorMessage: "agent failed"}}
	runner := DispatcherRunner{Client: client}

	result, err := runner.RunStep(context.Background(), StepRequest{WorkflowRunID: "po-run-1", StepID: "review", Prompt: "prompt", Repo: "/repo", Branch: "branch", WorktreePath: "/worktree"})
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if result.State != store.StateFailed || result.Outcome != "agent failed" {
		t.Fatalf("result = %+v, want failed pd outcome", result)
	}
}

type recordingPDClient struct {
	request     PDStartRequest
	waitRequest PDWaitRequest
	startResult PDStartResult
	waitResult  PDTaskRunInfo
}

func (r *recordingPDClient) StartTaskRun(_ context.Context, req PDStartRequest) (PDStartResult, error) {
	r.request = req
	return r.startResult, nil
}

func (r *recordingPDClient) WaitTaskRun(_ context.Context, req PDWaitRequest) (PDTaskRunInfo, error) {
	r.waitRequest = req
	return r.waitResult, nil
}
