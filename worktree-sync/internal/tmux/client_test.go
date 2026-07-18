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
	results []result
	calls   [][]string
}

func (r *runner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{dir, name}, args...))
	result := r.results[0]
	r.results = r.results[1:]
	return result.out, result.err
}
func (*runner) Interactive(context.Context, string, string, ...string) error { return nil }

func TestSnapshotParsesMetadataAndUsesDedicatedSocket(t *testing.T) {
	r := &runner{results: []result{
		{out: []byte("$1\n")}, {out: []byte("wts-repo\n")},
		{out: []byte("1\n")}, {out: []byte("identity\n")}, {out: []byte("session\n")}, {out: []byte("identity\n")},
		{out: []byte("@1\n")}, {out: []byte("base\n")}, {out: []byte("/repo\n")},
		{out: []byte("1\n")}, {out: []byte("identity\n")}, {out: []byte("base\n")}, {out: []byte("identity\n")},
	}}
	client := tmux.New(r, "test-socket", time.Second)
	snapshot, err := client.Snapshot(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, "@1", snapshot.Sessions[0].Windows[0].ID)
	for _, call := range r.calls {
		require.Equal(t, []string{"", "tmux", "-L", "test-socket"}, call[:4])
	}
}

func TestCreateSessionCleansUpWhenTaggingFails(t *testing.T) {
	r := &runner{results: []result{{out: []byte("$1|@1\n")}, {err: errors.New("tag failed")}, {}}}
	client := tmux.New(r, "test-socket", time.Second)
	_, err := client.CreateSession(context.Background(), "wts-r", tmux.Window{Name: "base", Path: "/repo", Metadata: tmux.Metadata{Schema: 1, Repository: "repo", Role: "base", Identity: "repo"}})
	require.Error(t, err)
	require.Contains(t, r.calls[len(r.calls)-1], "kill-session")
	require.Contains(t, r.calls[len(r.calls)-1], "$1")
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
