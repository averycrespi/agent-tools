package catalog

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
)

type ProbePage func(context.Context, string, json.RawMessage, string) (downstream.Response, oauth.ProbeResult, error)

type OAuthProbeClient struct {
	mu          sync.Mutex
	serverID    string
	priorScopes []string
	probe       ProbePage
	refresh     oauth.RefreshInvoker
	stepUp      oauth.StepUpSink
	first       bool
}

func NewOAuthProbeClient(serverID string, priorScopes []string, probe ProbePage, refresh oauth.RefreshInvoker, stepUp oauth.StepUpSink) *OAuthProbeClient {
	return &OAuthProbeClient{serverID: serverID, priorScopes: append([]string(nil), priorScopes...), probe: probe, refresh: refresh, stepUp: stepUp, first: true}
}

func (client *OAuthProbeClient) Request(ctx context.Context, method string, params json.RawMessage, name string) (downstream.Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.probe == nil {
		return downstream.Response{}, oauth.ErrProbeRejected
	}
	probe := &pageProbe{ctx: ctx, method: method, params: append(json.RawMessage(nil), params...), name: name, request: client.probe}
	if client.first {
		client.first = false
		if err := oauth.ProbeWithRefresh(ctx, client.serverID, client.priorScopes, probe, client.refresh, client.stepUp); err != nil {
			return downstream.Response{}, err
		}
		return probe.response, nil
	}
	result, err := probe.Probe(ctx)
	if err != nil || result.Outcome != oauth.ProbeSucceeded {
		return downstream.Response{}, oauth.ErrProbeRejected
	}
	return probe.response, nil
}

type pageProbe struct {
	ctx      context.Context
	method   string
	params   json.RawMessage
	name     string
	request  ProbePage
	response downstream.Response
}

func (probe *pageProbe) Probe(context.Context) (oauth.ProbeResult, error) {
	response, result, err := probe.request(probe.ctx, probe.method, probe.params, probe.name)
	if err == nil && result.Outcome == oauth.ProbeSucceeded {
		probe.response = response
	}
	return result, err
}
