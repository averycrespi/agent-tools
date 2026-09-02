//go:build darwin || linux

package runtimes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcreteDriverNegotiatesRealStdioAndCatalogRequest(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		era    downstream.Era
		starts int
	}{
		{name: "modern", mode: "modern", era: downstream.EraModern, starts: 1},
		{name: "auto fallback", mode: "auto", era: downstream.EraLegacy, starts: 2},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executable, err := os.Executable()
			require.NoError(t, err)
			marker := filepath.Join(t.TempDir(), "fallback-marker")
			candidate := ownerCandidate(300+index, contract.TransportStdio)
			candidate.Server.Transport = mustDriverTransport(t, contract.StdioTransport{
				Kind: contract.TransportStdio, Executable: executable,
				Arguments:        []string{"-test.run=^TestStdioFixtureProcess$", "--", "mcp", test.mode, marker},
				WorkingDirectory: t.TempDir(), Environment: map[string]string{stdioFixtureMarker: "1"}, SecretEnvironment: map[string]string{},
			})
			supervisor := NewStdioSupervisor(nil)
			starts := 0
			reports := make(chan FailureDisposition, 1)
			driver, err := NewConcreteDriver(ConcreteDriverOptions{
				Owner: NewRuntimeOwner(),
				StartStdio: func(ctx context.Context, definition StdioDefinition) (downstream.StdioRuntime, error) {
					if starts > 0 {
						assert.Equal(t, int64(0), supervisor.Status().InUse, "fallback process started before probe reap")
					}
					starts++
					return supervisor.Start(ctx, definition)
				},
				HTTPFactory: remote.New(remote.Options{}),
				ReportFailure: func(Candidate, FailureDisposition) bool {
					reports <- FailureDisposition{RuntimeLost: true}
					return true
				},
			})
			require.NoError(t, err)

			outcome := driver.Reconcile(context.Background(), candidate, nil)

			assert.Equal(t, contract.RuntimeActive, outcome.State)
			assert.Equal(t, test.starts, starts)
			runtime, ok := driver.Runtime(candidate)
			require.True(t, ok)
			assert.Equal(t, test.era, runtime.Era())
			response, err := runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
			require.NoError(t, err)
			assert.Contains(t, string(response.Result), `"name":"fixture"`)
			assert.True(t, driver.Stop(context.Background(), candidate))
			assert.Equal(t, int64(0), supervisor.Status().InUse)
			select {
			case <-reports:
				t.Fatal("deliberate stop reported a runtime failure")
			default:
			}
		})
	}
}
