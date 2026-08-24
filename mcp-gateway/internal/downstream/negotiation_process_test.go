//go:build darwin || linux

package downstream_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cancelAfterExchangeTransport struct {
	downstream.Transport
	cancel context.CancelFunc
}

func (transport cancelAfterExchangeTransport) Exchange(ctx context.Context, message downstream.Message) (downstream.WireResponse, error) {
	response, err := transport.Transport.Exchange(ctx, message)
	transport.cancel()
	return response, err
}

func TestRealProcessAutoFallbackRequiresProbeReapBeforeLegacyConstruction(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	supervisor := runtimes.NewStdioSupervisor(nil)
	initializationCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var process *runtimes.StdioRuntime
	opened := 0
	negotiator, err := downstream.NewNegotiatorWithDeadline(func(context.Context) (*downstream.Coordinator, error) {
		opened++
		if opened > 1 {
			t.Fatal("legacy process constructed after unconfirmed probe reap")
		}
		process, err = supervisor.Start(context.Background(), runtimes.StdioDefinition{
			RuntimeID:        "real-fallback-probe",
			Executable:       executable,
			Arguments:        []string{"-test.run=^TestMCPFallbackProbeProcess$"},
			WorkingDirectory: t.TempDir(),
			Environment:      map[string]string{"MCP_FALLBACK_PROBE_PROCESS": "1"},
		})
		if err != nil {
			return nil, err
		}
		stdio, err := downstream.NewStdioTransport(process)
		if err != nil {
			return nil, err
		}
		return downstream.NewCoordinator(cancelAfterExchangeTransport{Transport: stdio, cancel: cancel})
	}, func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
		return initializationCtx, func() {}
	})
	require.NoError(t, err)
	_, err = negotiator.Negotiate(context.Background(), downstream.ModeAuto)
	assert.ErrorIs(t, err, downstream.ErrStopUnconfirmed)
	assert.Equal(t, 1, opened)
	require.NotNil(t, process)
	select {
	case <-process.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("fallback probe process was not reaped after forced stop")
	}
	assert.Equal(t, int64(0), supervisor.Status().InUse)
}

func TestMCPFallbackProbeProcess(t *testing.T) {
	if os.Getenv("MCP_FALLBACK_PROBE_PROCESS") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`)
	blocked := make(chan os.Signal, 1)
	signal.Notify(blocked, syscall.SIGUSR1)
	<-blocked
}
