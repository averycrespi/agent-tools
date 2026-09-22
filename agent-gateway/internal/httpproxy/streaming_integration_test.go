//go:build integration

package httpproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntegrationEarlyResponseJoinsBlockedUpload(t *testing.T) {
	f := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		_ = controller.EnableFullDuplex()
		w.WriteHeader(413)
		_, _ = io.WriteString(w, "stop")
		_ = controller.Flush()
	}))
	defer upstream.Close()
	f.allow(t, upstream.URL, "allow_requests", "", "")
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	conn, err := net.DialTimeout("tcp", f.address, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = fmt.Fprintf(conn, "POST %s/ HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\nContent-Length: 1048576\r\n\r\nx", upstream.URL, u.Host, f.credential.Bearer)
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	require.NoError(t, err)
	require.Equal(t, 413, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "stop", string(body))
	require.NoError(t, response.Body.Close())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, f.engine.Close(ctx))
}

func TestIntegrationSSEIsIncrementalAndCancellationCleansUp(t *testing.T) {
	f := fixture(t)
	events := make(chan struct{}, 1)
	stopped := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(stopped)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; ; i++ {
			_, err := fmt.Fprintf(w, "data: %d\n\n", i)
			if err != nil {
				return
			}
			if http.NewResponseController(w).Flush() != nil {
				return
			}
			select {
			case <-events:
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer upstream.Close()
	f.allow(t, upstream.URL, "allow_requests", "", "")
	f.engine.afterFunc = func(time.Duration, func()) func() { panic("intercepted stream must not arm tunnel lifetime") }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", upstream.URL+"/events", nil)
	require.NoError(t, err)
	req.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
	response, err := f.client(t).Do(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	reader := bufio.NewReader(response.Body)
	for i := 0; i < 3; i++ {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("data: %d\n", i), line)
		_, err = reader.ReadString('\n')
		require.NoError(t, err)
		events <- struct{}{}
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream SSE did not cancel")
	}
}
