package httpproxy

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestH2IncompleteHeaderBounds(t *testing.T) {
	for _, data := range [][]byte{{0}, {0, 0, 1, 1, 0, 0, 0, 0, 1}, {0, 0, 0, 1, 0, 0, 0, 0, 1}} {
		t.Run(string(rune('a'+len(data))), func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			bounded := &h2BoundConn{Conn: server, timeout: 25 * time.Millisecond}
			done := make(chan error, 1)
			go func() { _, err := io.Copy(io.Discard, bounded); done <- err }()
			_, err := client.Write(data)
			require.NoError(t, err)
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(time.Second):
				t.Fatal("partial header phase did not expire")
			}
		})
	}
}

func TestH2CompletedFramesDoNotCapStreamLifetime(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	bounded := &h2BoundConn{Conn: server, timeout: 25 * time.Millisecond}
	// Complete empty HEADERS with END_HEADERS, followed by a complete DATA frame.
	headers := []byte{0, 0, 0, 1, 4, 0, 0, 0, 1}
	data := []byte{0, 0, 1, 0, 0, 0, 0, 0, 1, 'x'}
	consumed := make(chan struct{})
	sink := &h2FrameCompletionWriter{remaining: len(headers) + len(data), done: consumed}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(sink, bounded); done <- err }()
	_, err := client.Write(headers)
	require.NoError(t, err)
	select {
	case err := <-done:
		t.Fatalf("completed phase retained deadline: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	_, err = client.Write(data)
	require.NoError(t, err)
	// A pipe write acknowledges bytes before Read finishes clearing its deadline.
	// The copy sink sees those bytes only after the bounded Read has returned.
	select {
	case <-consumed:
	case err := <-done:
		t.Fatalf("frame consumption ended early: %v", err)
	case <-time.After(time.Second):
		t.Fatal("complete DATA frame was not consumed")
	}
	require.NoError(t, client.Close())
	require.NoError(t, <-done)
}

type h2FrameCompletionWriter struct {
	remaining int
	done      chan struct{}
}

func (w *h2FrameCompletionWriter) Write(p []byte) (int, error) {
	w.remaining -= len(p)
	if w.remaining == 0 {
		close(w.done)
	}
	return len(p), nil
}
