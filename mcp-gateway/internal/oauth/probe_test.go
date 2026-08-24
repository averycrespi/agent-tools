package oauth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeRefreshesAndRetriesOnlyRecognizedInvalidTokenOnce(t *testing.T) {
	probe := &probeFake{results: []ProbeResult{{Outcome: ProbeInvalidToken, ChallengeMetadata: []string{"https://resource.example/meta"}}, {Outcome: ProbeSucceeded}}}
	refresh := &refreshInvokerFake{}
	err := ProbeWithRefresh(context.Background(), "server", []string{"read"}, probe, refresh, &stepUpFake{})
	require.NoError(t, err)
	assert.Equal(t, 2, probe.calls)
	assert.Equal(t, 1, refresh.calls)
	assert.True(t, refresh.request.ForceInvalidToken)
}

func TestProbeInsufficientScopeStagesSortedForegroundHandoffWithoutRefreshOrReplay(t *testing.T) {
	probe := &probeFake{results: []ProbeResult{{Outcome: ProbeInsufficientScope, ChallengeScopes: []string{"write"}}}}
	refresh := &refreshInvokerFake{}
	stepUp := &stepUpFake{}
	err := ProbeWithRefresh(context.Background(), "server", []string{"read"}, probe, refresh, stepUp)
	assert.ErrorIs(t, err, ErrRefreshReauthorization)
	assert.Equal(t, 1, probe.calls)
	assert.Zero(t, refresh.calls)
	assert.Equal(t, []string{"read"}, stepUp.prior)
	assert.Equal(t, []string{"write"}, stepUp.challenge)
}

type probeFake struct {
	results []ProbeResult
	calls   int
}

func (probe *probeFake) Probe(context.Context) (ProbeResult, error) {
	result := probe.results[probe.calls]
	probe.calls++
	return result, nil
}

type refreshInvokerFake struct {
	calls   int
	request RefreshRequest
}

func (refresh *refreshInvokerFake) Refresh(_ context.Context, request RefreshRequest) (RefreshResult, error) {
	refresh.calls++
	refresh.request = request
	return RefreshResult{Refreshed: true}, nil
}

type stepUpFake struct{ prior, challenge []string }

func (step *stepUpFake) StageStepUp(_ string, _ []string, prior, challenge []string) error {
	step.prior = prior
	step.challenge = challenge
	return nil
}
