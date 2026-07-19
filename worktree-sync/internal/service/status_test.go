package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

func TestStatusV2HumanOutputUsesProgressiveDetail(t *testing.T) {
	document := newStatusDocument()
	document.Repositories = append(document.Repositories,
		statusRepository{ID: "healthy", Health: healthHealthy, DesiredWorktrees: []statusWorktree{{Path: "/healthy", Eligible: true}}, ActualManagedWindows: []statusWindow{}, Diagnostics: []statusDiagnostic{}, Conflicts: []string{}, ReportedWorktrees: []statusReportedWorktree{}, PrunableWorktrees: []statusReportedWorktree{}, ActionFailures: []statusActionFailure{}},
		statusRepository{ID: "attention", Health: healthAttention, DesiredWorktrees: []statusWorktree{}, ActualManagedWindows: []statusWindow{}, Diagnostics: []statusDiagnostic{{Severity: severityWarning, Code: codeWorktreeOutsideRoots, Message: "worktree is outside allowed roots", Recovery: "add an allowed root"}}, Conflicts: []string{}, ReportedWorktrees: []statusReportedWorktree{{Path: "/outside", Reason: reasonOutsideRoots}}, PrunableWorktrees: []statusReportedWorktree{}, ActionFailures: []statusActionFailure{}},
	)

	concise := renderStatusHuman(document, false)
	require.Contains(t, concise, "healthy\thealthy")
	require.NotContains(t, concise, "/healthy")
	require.Contains(t, concise, "warning[worktree_outside_allowed_roots]")
	require.Contains(t, concise, "/outside")
	verbose := renderStatusHuman(document, true)
	require.Contains(t, verbose, "desired: /healthy")
	require.True(t, statusRequiresAttention(document))
}

func TestStatusV2SortsPublicCollectionsAndOmitsEmptyOptionalFields(t *testing.T) {
	document := newStatusDocument()
	document.Diagnostics = []statusDiagnostic{{Severity: severityWarning, Code: "z", Message: "z"}, {Severity: severityError, Code: "a", Message: "a"}}
	document.Repositories = []statusRepository{
		{ID: "z", Health: healthHealthy, Diagnostics: []statusDiagnostic{}, DesiredWorktrees: []statusWorktree{}, ActualManagedWindows: []statusWindow{}, Conflicts: []string{}, ReportedWorktrees: []statusReportedWorktree{}, PrunableWorktrees: []statusReportedWorktree{}, ActionFailures: []statusActionFailure{}},
		{ID: "a", Health: healthAttention, Diagnostics: []statusDiagnostic{}, DesiredWorktrees: []statusWorktree{{Path: "/z", Identity: "z", Eligible: true}, {Path: "/a", Identity: "a", Eligible: true}}, ActualManagedWindows: []statusWindow{{ID: "@2"}, {ID: "@1"}}, Conflicts: []string{"z", "a"}, ReportedWorktrees: []statusReportedWorktree{}, PrunableWorktrees: []statusReportedWorktree{}, ActionFailures: []statusActionFailure{{WorktreeIdentity: "worktree", Action: "unknown", Attempted: "2026-01-01T00:00:00Z", ErrorCode: "legacy_failure", Message: "recorded action failure"}}},
	}
	sortStatusDocument(&document)
	require.Equal(t, "a", document.Repositories[0].ID)
	require.Equal(t, "/a", document.Repositories[0].DesiredWorktrees[0].Path)
	require.Equal(t, "@1", document.Repositories[0].ActualManagedWindows[0].ID)
	require.Equal(t, []string{"a", "z"}, document.Repositories[0].Conflicts)
	data, err := json.Marshal(document)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	firstDesired := decoded["repositories"].([]any)[0].(map[string]any)["desired_worktrees"].([]any)[0].(map[string]any)
	require.NotContains(t, firstDesired, "branch")
	require.NotContains(t, firstDesired, "locked")
	require.NotContains(t, decoded["daemon"].(map[string]any), "message")
}

func TestUnknownLegacyActionFailureHasSafeRecoveryGuidance(t *testing.T) {
	repo := config.Repository{ID: "api", CommonGitDir: "/repo/.git"}
	failure := actionFailure(repo, state.ActionKey{Repository: repo.Identity(), Worktree: "worktree", Trigger: "explicit", Digest: "obsolete"}, state.ActionResult{}, nil)
	require.Equal(t, "unknown", failure.Action)
	require.Contains(t, failure.Message, "wts reconcile --repo-id api")
}

func TestStatusV2JSONUsesStableArraysAndTypedDaemon(t *testing.T) {
	document := newStatusDocument()
	data, err := json.Marshal(document)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, float64(2), decoded["version"])
	require.Equal(t, []any{}, decoded["repositories"])
	require.Equal(t, []any{}, decoded["diagnostics"])
	require.Equal(t, map[string]any{"state": "unsupported"}, decoded["daemon"])
	require.False(t, statusRequiresAttention(document))
}
