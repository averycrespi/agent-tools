//go:build darwin || linux

package runtimes

import (
	"bytes"
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
)

func TestStdioInitiatingFrameFailureReachesRuntimeDefaultSink(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	repository := newFakeRepository(1)
	var id string
	for key, server := range repository.servers {
		id = key
		server.Transport = mustDriverTransport(t, contract.StdioTransport{Kind: contract.TransportStdio, Executable: executable, Arguments: []string{"-test.run=^TestStdioFixtureProcess$", "--", "mcp", "fault"}, WorkingDirectory: t.TempDir(), Environment: map[string]string{stdioFixtureMarker: "1"}, SecretEnvironment: map[string]string{}})
		repository.servers[key] = server
	}
	supervisor := NewStdioSupervisor(nil)
	started := make(chan *StdioRuntime, 1)
	var manager *Manager
	driver, err := NewConcreteDriver(ConcreteDriverOptions{Owner: NewRuntimeOwner(), HTTPFactory: remote.New(remote.Options{}), StartStdio: func(ctx context.Context, definition StdioDefinition) (downstream.StdioRuntime, error) {
		definition.SecretEnvironment = map[string]string{"RUNTIME_SECRET": "fixture"}
		definition.Secrets = map[string]string{"fixture": "actual-child-credential-canary"}
		runtime, err := supervisor.Start(ctx, definition)
		if err == nil {
			started <- runtime
		}
		return runtime, err
	}, ReportFailure: func(candidate Candidate, failure FailureDisposition) bool {
		return manager.ReportRuntimeFailure(candidate, failure)
	}})
	require.NoError(t, err)
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
	manager, err = New(Options{Repository: repository, Driver: driver, Catalog: diagnosticCatalog{}, Scheduler: newFakeScheduler(), Diagnostics: adapter})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() { <-manager.Drain(context.Background()) })
	manager.Trigger(id, nil, true)
	require.True(t, manager.Wait(ctx))
	require.Equal(t, contract.RuntimeActive, manager.Status(id).State)
	runtime := <-started
	require.NoError(t, runtime.command.Process.Signal(syscall.SIGUSR1))
	require.Eventually(t, func() bool { return manager.Status(id).State == contract.RuntimeRetryWait }, 5*time.Second, time.Millisecond)
	<-manager.Drain(ctx)
	require.Zero(t, supervisor.Status().InUse)
	require.True(t, adapter.Finish(nil))
	require.Contains(t, output.String(), "stream=stdout rule=frame_bytes")
	require.Contains(t, output.String(), "observed_bytes=")
	require.Contains(t, output.String(), "allowed_bytes=")
	require.Contains(t, output.String(), "requested termination")
	require.NotContains(t, output.String(), "private-stdout-canary")
	require.NotContains(t, output.String(), "actual-child-credential-canary")
}
