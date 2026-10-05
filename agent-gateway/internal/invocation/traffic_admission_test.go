package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

func TestTrafficLocalUnknownDoesNotInventTerminal(t *testing.T) {
	traffic, _ := trafficFixture(t, nil, nil)
	observation := recordMCP(t, traffic, trafficPrepared(1))
	service := &Service{audits: &Repository{traffic: traffic}}
	response := service.finish(t.Context(), invocationID(1), observation, CallOutcome{ErrorCode: contract.ToolUnavailable})
	require.Equal(t, contract.ToolUnavailable, response.ErrorCode)
	history, err := traffic.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.Nil(t, history.Records[0].CompletedAt)
}

func TestMCPExecutionAndResponseDoNotWaitForOptionalHistory(t *testing.T) {
	for _, mode := range []string{"stalled", "full", "unavailable", "uncertain", "identity failure"} {
		t.Run(mode, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var once sync.Once
			traffic, _ := trafficFixture(t, func(c *TrafficConfig) {
				if mode == "full" {
					c.QueueRecords = 1
					c.BatchRecords = 1
				}
			}, func(point string) error {
				if point == "before_begin" {
					once.Do(func() { close(entered); <-release })
				}
				if point == "acknowledgment" && mode == "uncertain" {
					return errors.New("uncertain history")
				}
				return nil
			})
			audits.traffic = traffic
			if mode == "unavailable" {
				traffic.BeginDrain()
			}
			if mode == "identity failure" {
				audits.entropy = emptyEntropy{}
			}
			if mode == "full" {
				require.NotNil(t, traffic.ObserveMCP(trafficPrepared(77)))
				<-entered
			}
			lease, err := authority.Authenticate(t.Context(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			calls := 0
			service, err := newService(audits, authority, func(string) (callTarget, bool) {
				return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
					return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
						calls++
						return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
					}}, nil
				}), true
			})
			require.NoError(t, err)
			done := make(chan CallResponse, 1)
			go func() { done <- service.Call(t.Context(), lease, validCallParams()) }()
			select {
			case response := <-done:
				require.NotNil(t, response.Result)
				require.Empty(t, response.ErrorCode)
			case <-time.After(time.Second):
				t.Fatal("MCP response waited for history")
			}
			require.Equal(t, 1, calls)
			unblock()
			waitTraffic(t, traffic)
			if mode == "uncertain" {
				require.False(t, traffic.Healthy())
			}
		})
	}
}

type emptyEntropy struct{}

func (emptyEntropy) Read([]byte) (int, error) { return 0, errors.New("capture entropy unavailable") }
