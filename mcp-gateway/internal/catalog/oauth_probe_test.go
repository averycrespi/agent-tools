package catalog

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthCatalogProbeRefreshesAndRetriesOnlyFirstPageOnce(t *testing.T) {
	calls := 0
	refresh := new(refreshFake)
	client := NewOAuthProbeClient("server", []string{"read"}, func(context.Context, string, json.RawMessage, string) (downstream.Response, oauth.ProbeResult, error) {
		calls++
		if calls == 1 {
			return downstream.Response{}, oauth.ProbeResult{Outcome: oauth.ProbeInvalidToken}, nil
		}
		return downstream.Response{Result: json.RawMessage(`{"tools":[]}`)}, oauth.ProbeResult{Outcome: oauth.ProbeSucceeded}, nil
	}, refresh, new(stepUpFake))
	candidate, err := NewTraverser().Traverse(context.Background(), client, "sample")
	require.NoError(t, err)
	assert.Equal(t, int64(1), candidate.Pages)
	assert.Equal(t, 2, calls)
	assert.Equal(t, 1, refresh.calls)
}

func TestOAuthCatalogProbeStagesInsufficientScopeWithoutRefreshOrReplay(t *testing.T) {
	calls := 0
	refresh := new(refreshFake)
	stepUp := new(stepUpFake)
	client := NewOAuthProbeClient("server", []string{"read"}, func(context.Context, string, json.RawMessage, string) (downstream.Response, oauth.ProbeResult, error) {
		calls++
		return downstream.Response{}, oauth.ProbeResult{Outcome: oauth.ProbeInsufficientScope, ChallengeMetadata: []string{"https://resource.example/metadata"}, ChallengeScopes: []string{"write"}}, nil
	}, refresh, stepUp)
	_, err := NewTraverser().Traverse(context.Background(), client, "sample")
	assert.ErrorIs(t, err, oauth.ErrRefreshReauthorization)
	assert.Equal(t, 1, calls)
	assert.Equal(t, 0, refresh.calls)
	assert.Equal(t, 1, stepUp.calls)
}

func TestOAuthCatalogProbeDoesNotRefreshLaterPage(t *testing.T) {
	calls := 0
	refresh := new(refreshFake)
	client := NewOAuthProbeClient("server", nil, func(context.Context, string, json.RawMessage, string) (downstream.Response, oauth.ProbeResult, error) {
		calls++
		if calls == 1 {
			return downstream.Response{Result: json.RawMessage(`{"tools":[],"nextCursor":"next"}`)}, oauth.ProbeResult{Outcome: oauth.ProbeSucceeded}, nil
		}
		return downstream.Response{}, oauth.ProbeResult{Outcome: oauth.ProbeInvalidToken}, nil
	}, refresh, new(stepUpFake))
	_, err := NewTraverser().Traverse(context.Background(), client, "sample")
	assert.ErrorIs(t, err, oauth.ErrProbeRejected)
	assert.Equal(t, 2, calls)
	assert.Equal(t, 0, refresh.calls)
}

type refreshFake struct{ calls int }

func (refresh *refreshFake) Refresh(context.Context, oauth.RefreshRequest) (oauth.RefreshResult, error) {
	refresh.calls++
	return oauth.RefreshResult{Refreshed: true}, nil
}

type stepUpFake struct{ calls int }

func (stepUp *stepUpFake) StageStepUp(string, []string, []string, []string) error {
	stepUp.calls++
	return nil
}
