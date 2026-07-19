package tmux_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

type result struct {
	out []byte
	err error
}
type runner struct {
	results     []result
	calls       [][]string
	interactive [][]string
}

func (r *runner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{dir, name}, args...))
	result := r.results[0]
	r.results = r.results[1:]
	return result.out, result.err
}
func (r *runner) Interactive(_ context.Context, dir, name string, args ...string) error {
	r.interactive = append(r.interactive, append([]string{dir, name}, args...))
	return nil
}

func TestSnapshotParsesMetadataAndUsesDedicatedSocket(t *testing.T) {
	details := "__WTS_DETAIL_session-name__\nwts-repo\n__WTS_DETAIL_session-metadata__\n" +
		"@wts-schema 1\n@wts-repository identity\n@wts-role session\n@wts-identity identity\n" +
		"__WTS_DETAIL_window-0-name__\nbase\n__WTS_DETAIL_window-0-path__\n/repo\n__WTS_DETAIL_window-0-metadata__\n" +
		"@wts-schema 1\n@wts-repository identity\n@wts-role base\n@wts-identity identity\n"
	r := &runner{results: []result{{out: []byte("$1\n")}, {out: []byte("@1\n")}, {out: []byte(details)}}}
	client := tmux.New(r, "test-socket", time.Second)
	snapshot, err := client.Snapshot(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, "@1", snapshot.Sessions[0].Windows[0].ID)
	for _, call := range r.calls {
		require.Equal(t, []string{"", "tmux", "-L", "test-socket"}, call[:4])
	}
}

func TestSessionOwnershipRecheckAndAttachUseStableID(t *testing.T) {
	expected := tmux.Metadata{Schema: tmux.MetadataSchema, Repository: "identity", Role: "session", Identity: "identity"}
	r := &runner{results: []result{{out: []byte("@wts-schema 1\n@wts-repository identity\n@wts-role session\n@wts-identity identity\n")}}}
	client := tmux.New(r, "test-socket", time.Second)
	owned, err := client.OwnsSession(context.Background(), "$2", expected)
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, client.Attach(context.Background(), "$2"))
	require.Equal(t, [][]string{{"", "tmux", "-L", "test-socket", "attach-session", "-t", "$2"}}, r.interactive)
}

func TestCreateSessionCleansUpWhenTaggingFails(t *testing.T) {
	r := &runner{results: []result{{out: []byte("$1|@1\n")}, {err: errors.New("tag failed")}, {}}}
	client := tmux.New(r, "test-socket", time.Second)
	_, err := client.CreateSession(context.Background(), "wts-r", tmux.Window{Name: "base", Path: "/repo", Metadata: tmux.Metadata{Schema: 1, Repository: "repo", Role: "base", Identity: "repo"}})
	require.Error(t, err)
	require.Contains(t, r.calls[len(r.calls)-1], "kill-session")
	require.Contains(t, r.calls[len(r.calls)-1], "$1")

	r = &runner{results: []result{{out: []byte("$2|@2\n")}, {err: errors.New("tag failed")}, {err: errors.New("cleanup failed")}}}
	client = tmux.New(r, "test-socket", time.Second)
	id, err := client.CreateSession(context.Background(), "wts-r", tmux.Window{Name: "base", Path: "/repo", Metadata: tmux.Metadata{Schema: 1, Repository: "repo", Role: "base", Identity: "repo"}})
	require.Equal(t, "$2", id)
	require.ErrorContains(t, err, "may remain")
	require.ErrorContains(t, err, "cleanup failed")
}

func TestCreateWindowReportsFailedTagCleanup(t *testing.T) {
	r := &runner{results: []result{{out: []byte("@2\n")}, {err: errors.New("tag failed")}, {err: errors.New("cleanup failed")}}}
	id, err := tmux.New(r, "test-socket", time.Second).CreateWindow(context.Background(), "$1", tmux.Window{Name: "feature", Path: "/repo", Metadata: tmux.Metadata{Schema: 1, Repository: "repo", Role: "worktree", Identity: "worktree"}})
	require.Equal(t, "@2", id)
	require.ErrorContains(t, err, "may remain")
	require.ErrorContains(t, err, "cleanup failed")
}

func TestNameRepairDoesNotRespawnWindow(t *testing.T) {
	metadata := []byte("@wts-schema 1\n@wts-repository repo\n@wts-role base\n@wts-identity repo\n")
	r := &runner{results: []result{{out: metadata}, {}}}
	client := tmux.New(r, "test-socket", time.Second)
	current := tmux.Window{Name: "old-name", Path: "/repo", Metadata: tmux.Metadata{Schema: 1, Repository: "repo", Role: "base", Identity: "repo"}}
	desired := current
	desired.Name = "new-name"
	repairResult, err := client.RepairWindow(context.Background(), "@1", current, desired)
	require.NoError(t, err)
	require.True(t, repairResult.Renamed)
	require.False(t, repairResult.Respawned)
	for _, call := range r.calls {
		require.NotContains(t, call, "respawn-window")
	}
}

func TestRepairWindowRechecksOwnershipBetweenEveryMutation(t *testing.T) {
	metadata := []byte("@wts-schema 1\n@wts-repository repo\n@wts-role base\n@wts-identity repo\n")
	changed := []byte("@wts-schema 1\n@wts-repository other\n@wts-role base\n@wts-identity repo\n")
	current := tmux.Window{Name: "old", Path: "/old", Metadata: tmux.Metadata{Schema: 1, Repository: "repo", Role: "base", Identity: "repo"}}
	desired := current
	desired.Name, desired.Path = "new", "/new"

	r := &runner{results: []result{{out: metadata}, {}, {out: changed}}}
	repairResult, err := tmux.New(r, "test-socket", time.Second).RepairWindow(context.Background(), "@1", current, desired)
	require.ErrorContains(t, err, "ownership changed")
	require.True(t, repairResult.Renamed)
	require.False(t, repairResult.Respawned)
	require.Len(t, r.calls, 3)

	desired.Name = current.Name
	desired.Metadata.Repository = "new-repo"
	r = &runner{results: []result{{out: metadata}, {}, {out: changed}}}
	repairResult, err = tmux.New(r, "test-socket", time.Second).RepairWindow(context.Background(), "@1", current, desired)
	require.ErrorContains(t, err, "ownership changed")
	require.True(t, repairResult.Respawned)
	require.Zero(t, repairResult.MetadataOptions)
	require.Len(t, r.calls, 3)

	desired.Path = current.Path
	r = &runner{results: []result{{out: metadata}, {}, {out: changed}}}
	repairResult, err = tmux.New(r, "test-socket", time.Second).RepairWindow(context.Background(), "@1", current, desired)
	require.ErrorContains(t, err, "ownership changed")
	require.Equal(t, 1, repairResult.MetadataOptions)
	require.Len(t, r.calls, 3)
}

func TestTmuxErrorIncludesDiagnosticWithoutLaunchCommand(t *testing.T) {
	expected := tmux.Metadata{Schema: 1, Repository: "repo", Role: "worktree", Identity: "worktree"}
	metadata := []byte("@wts-schema 1\n@wts-repository repo\n@wts-role worktree\n@wts-identity worktree\n")
	r := &runner{results: []result{{out: metadata}, {out: []byte("tmux target unavailable: token=secret"), err: errors.New("exit")}}}
	client := tmux.New(r, "test-socket", time.Second)
	err := client.Launch(context.Background(), "@1", expected, "token=secret")
	require.Error(t, err)
	require.Contains(t, err.Error(), "tmux target unavailable")
	require.NotContains(t, err.Error(), "token=secret")
	require.NotContains(t, err.Error(), "token=")
	require.Contains(t, err.Error(), "[redacted launch command]")
	var delivery *tmux.LaunchError
	require.ErrorAs(t, err, &delivery)
	require.Equal(t, "unknown", delivery.TextSent)
	require.Equal(t, "no", delivery.EnterSent)

	r = &runner{results: []result{{out: metadata}, {}, {out: metadata}, {out: []byte("enter failed"), err: errors.New("exit")}}}
	delivery = nil
	err = tmux.New(r, "test-socket", time.Second).Launch(context.Background(), "@1", expected, "safe command")
	require.ErrorAs(t, err, &delivery)
	require.Equal(t, "yes", delivery.TextSent)
	require.Equal(t, "unknown", delivery.EnterSent)

	changed := []byte("@wts-schema 1\n@wts-repository other\n@wts-role worktree\n@wts-identity worktree\n")
	r = &runner{results: []result{{out: metadata}, {}, {out: changed}}}
	delivery = nil
	err = tmux.New(r, "test-socket", time.Second).Launch(context.Background(), "@1", expected, "safe command")
	require.ErrorContains(t, err, "refusing Enter")
	require.ErrorAs(t, err, &delivery)
	require.Equal(t, "yes", delivery.TextSent)
	require.Equal(t, "no", delivery.EnterSent)
	require.Len(t, r.calls, 3)
}

func TestApplyTagsCreatedObjectsAndKillsOnlyTargetID(t *testing.T) {
	r := &runner{results: make([]result, 11)}
	r.results[0].out = []byte("$1|@1\n")
	client := tmux.New(r, "test-socket", time.Second)
	meta := tmux.Metadata{Schema: 1, Repository: "repo", Role: "base", Identity: "repo"}
	_, err := client.CreateSession(context.Background(), "wts-r", tmux.Window{Name: "base", Path: "/repo", Metadata: meta})
	require.NoError(t, err)
	require.Contains(t, r.calls[0], "new-session")
	require.Contains(t, r.calls[0], "-c")
	for _, option := range []string{"@wts-schema", "@wts-repository", "@wts-role", "@wts-identity"} {
		found := false
		for _, call := range r.calls {
			for _, arg := range call {
				if arg == option {
					found = true
				}
			}
		}
		require.True(t, found, option)
	}

	r = &runner{results: []result{{}}}
	client = tmux.New(r, "test-socket", time.Second)
	require.NoError(t, client.KillWindow(context.Background(), "@9"))
	require.Equal(t, []string{"", "tmux", "-L", "test-socket", "kill-window", "-t", "@9"}, r.calls[0])
}
