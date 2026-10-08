//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
)

type tunnelDeadlineListener struct {
	net.Listener
	fail *atomic.Bool
}

func (l *tunnelDeadlineListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &tunnelDeadlineConn{Conn: c, fail: l.fail}, nil
}

type tunnelDeadlineConn struct {
	net.Conn
	fail *atomic.Bool
}

func (c *tunnelDeadlineConn) SetReadDeadline(deadline time.Time) error {
	if c.fail.Load() {
		return errors.New("set read deadline: closed connection Authorization: Bearer tunnel-secret-canary")
	}
	return c.Conn.SetReadDeadline(deadline)
}

type tunnelWriteFailureConn struct {
	net.Conn
	writes *atomic.Int64
}

func (c *tunnelWriteFailureConn) Write([]byte) (int, error) {
	c.writes.Add(1)
	return 0, errors.New("write upstream: broken pipe Authorization: Bearer tunnel-secret-canary")
}

func TestIntegrationTunnelFailureObservedOperation(t *testing.T) {
	for _, stage := range []string{"upstream_write", "deadline"} {
		t.Run(stage, func(t *testing.T) {
			var fail atomic.Bool
			f := fixtureWithListener(t, nil, func(l net.Listener) net.Listener { return &tunnelDeadlineListener{Listener: l, fail: &fail} })
			var output bytes.Buffer
			observer := diagnostics.New(&output, diagnostics.Warn)
			f.engine.options.Diagnostics = observer
			t.Cleanup(func() { observer.Finish(nil); <-observer.Done() })
			var calls, dials, writes atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			f.allow(t, upstream.URL, "allow_tunnel", "", "")
			f.engine.options.Remote = remote.New(remote.Options{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				dials.Add(1)
				c, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err == nil && stage == "upstream_write" {
					return &tunnelWriteFailureConn{Conn: c, writes: &writes}, nil
				}
				return c, err
			}})
			target, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			client, err := net.DialTimeout("tcp", f.address, time.Second)
			require.NoError(t, err)
			defer func() { _ = client.Close() }()
			require.NoError(t, client.SetDeadline(time.Now().Add(5*time.Second)))
			_, err = fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", target.Host, target.Host, f.credential.Bearer)
			require.NoError(t, err)
			reader := bufio.NewReader(client)
			response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
			require.NoError(t, err)
			require.Equal(t, 200, response.StatusCode)
			if stage == "deadline" {
				fail.Store(true)
			}
			_, err = io.WriteString(client, "x")
			require.NoError(t, err)
			trailing, readErr := io.ReadAll(reader)
			// TCP stacks can expose either clean EOF or a reset after the abort.
			if readErr != nil {
				var nerr net.Error
				require.ErrorAs(t, readErr, &nerr)
				require.False(t, nerr.Timeout())
			}
			require.NotContains(t, string(trailing), "HTTP/1.1")
			require.EqualValues(t, 1, dials.Load())
			require.Zero(t, calls.Load())
			if stage == "upstream_write" {
				require.EqualValues(t, 1, writes.Load())
			}
			require.Eventually(t, func() bool { f.engine.mu.Lock(); defer f.engine.mu.Unlock(); return f.engine.work == 0 }, time.Second, time.Millisecond)
			require.True(t, observer.Finish(nil))
			require.Contains(t, output.String(), `"stage":"`+stage+`"`)
			require.NotContains(t, output.String(), `"stage":"upstream_read"`)
			require.NotContains(t, output.String(), "tunnel-secret-canary")
			require.NotContains(t, output.String(), f.credential.Bearer)
			require.Contains(t, output.String(), target.Host)
			if stage == "deadline" {
				require.Contains(t, output.String(), "set read deadline: closed connection")
			} else {
				require.Contains(t, output.String(), "write upstream: broken pipe")
			}
		})
	}
}
