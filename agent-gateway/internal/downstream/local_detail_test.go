package downstream

import (
	"context"
	"io"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

type formattingWriteError struct {
	transport *StdioTransport
	unlocked  bool
}

func (e *formattingWriteError) Error() string {
	if !e.transport.exchangeMu.TryLock() {
		return "formatter called under exchange lock"
	}
	e.transport.exchangeMu.Unlock()
	e.unlocked = true
	return "write pipe: permission denied"
}

type failingDiagnosticStdio struct{ err error }

func (*failingDiagnosticStdio) Frames() <-chan []byte       { return nil }
func (r *failingDiagnosticStdio) Input() io.WriteCloser     { return r }
func (*failingDiagnosticStdio) Stop(context.Context) bool   { return true }
func (r *failingDiagnosticStdio) Write([]byte) (int, error) { return 0, r.err }
func (*failingDiagnosticStdio) Close() error                { return nil }

func TestStdioFailureFormattingAfterExchangeUnlock(t *testing.T) {
	for _, notify := range []bool{false, true} {
		runtime := &failingDiagnosticStdio{}
		transport, err := NewStdioTransport(runtime)
		require.NoError(t, err)
		cause := &formattingWriteError{transport: transport}
		runtime.err = cause
		if notify {
			_, err = transport.Notify(t.Context(), Message{Payload: []byte(`{}`)})
		} else {
			_, err = transport.Exchange(t.Context(), Message{Payload: []byte(`{}`)})
		}
		require.ErrorIs(t, err, ErrTransportClosed)
		require.True(t, cause.unlocked)
		require.Contains(t, diagnostics.Snapshot("mcp", "execute", "fixture", err).Explanation, "write pipe: permission denied")
	}
}
