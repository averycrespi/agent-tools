//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func assertFailureResponse(t *testing.T, response *http.Response, status int) string {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, status, response.StatusCode)
	require.Equal(t, http.StatusText(status)+"\n", string(body))
	require.Equal(t, "AgentGateway; error="+contract.HTTPProxyFailureReason(status), response.Header.Get("Proxy-Status"))
	id := response.Header.Get(contract.HTTPProxyCorrelationHeader)
	require.Regexp(t, "^[0-9a-f]{32}$", id)
	require.NotContains(t, string(body), "canary")
	return id
}

func TestIntegrationFailureContractPlainWire(t *testing.T) {
	for _, mode := range []string{"authentication", "invalid", "policy", "authority", "traffic", "connection", "timeout", "panic"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
			defer upstream.Close()
			var output bytes.Buffer
			observer := diagnostics.New(&output, diagnostics.Warn)
			f.engine.options.Diagnostics = observer
			t.Cleanup(func() { observer.Finish(nil); <-observer.Done() })
			expected := http.StatusForbidden
			target := upstream.URL + "/canary?private=canary"
			switch mode {
			case "authentication":
				expected = 407
			case "invalid":
				expected = 400
				target = upstream.URL + "/%5c?canary"
			case "authority":
				expected = 503
				f.engine.options.Ready = func() bool { return false }
			case "traffic":
				expected = 403
				f.traffic.BeginDrain()
			case "connection":
				expected = 502
				f.allow(t, upstream.URL, "allow_requests", "", "")
				upstream.Close()
			case "timeout":
				expected = 504
				f.allow(t, upstream.URL, "allow_requests", "", "")
				f.engine.options.Remote = remote.New(remote.Options{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }})
			case "panic":
				expected = 503
				f.engine.options.Ready = func() bool { panic("readiness check panicked Authorization: Bearer " + f.credential.Bearer) }
			}
			request, err := http.NewRequestWithContext(t.Context(), "GET", target, nil)
			require.NoError(t, err)
			if mode != "authentication" {
				request.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
			}
			request.Header.Set("Proxy-Status", "AgentGateway; error=spoof-canary")
			request.Header.Set(contract.HTTPProxyCorrelationHeader, "spoof-canary")
			response, err := f.client(t).Do(request)
			require.NoError(t, err)
			id := assertFailureResponse(t, response, expected)
			require.Zero(t, calls.Load())
			// Join the producer before inspecting the asynchronously encoded sink.
			require.Eventually(t, func() bool { f.engine.mu.Lock(); defer f.engine.mu.Unlock(); return f.engine.work == 0 }, time.Second, time.Millisecond)
			require.True(t, observer.Finish(nil))
			if mode == "connection" || mode == "timeout" || mode == "panic" {
				var record map[string]any
				require.NoError(t, json.Unmarshal(output.Bytes(), &record))
				require.Equal(t, id, record["proxy_id"])
			}
			if mode == "panic" {
				require.Contains(t, output.String(), "readiness check panicked")
				require.Contains(t, output.String(), `"stack":`)
			}
			require.NotContains(t, output.String(), "canary")
			require.NotContains(t, output.String(), f.credential.Bearer)
		})
	}
}

func TestIntegrationFailureProvenanceAndInterruptionWire(t *testing.T) {
	for _, protocol := range []string{"plain", "http/1.1", "h2"} {
		for _, interrupted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/interrupted=%t", protocol, interrupted), func(t *testing.T) {
				f := fixture(t)
				var calls atomic.Int64
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Header.Get("Proxy-Status") != "" || r.Header.Get(contract.HTTPProxyCorrelationHeader) != "" {
						t.Error("forwarded untrusted provenance")
					}
					w.Header().Set("Proxy-Status", "AgentGateway; error=spoof-canary")
					w.Header().Set(contract.HTTPProxyCorrelationHeader, "spoof-canary")
					if interrupted {
						w.Header().Set("Content-Length", "100")
					}
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, "upstream application response")
				})
				var upstream *httptest.Server
				if protocol == "plain" {
					upstream = httptest.NewServer(handler)
				} else {
					upstream = httptest.NewTLSServer(handler)
					f.engine.roots = x509.NewCertPool()
					f.engine.roots.AddCert(upstream.Certificate())
				}
				defer upstream.Close()
				f.allow(t, upstream.URL, "allow_requests", "", "")
				var output bytes.Buffer
				observer := diagnostics.New(&output, diagnostics.Warn)
				f.engine.options.Diagnostics = observer
				t.Cleanup(func() { observer.Finish(nil); <-observer.Done() })
				var response *http.Response
				var err error
				request, err := http.NewRequestWithContext(t.Context(), "GET", upstream.URL, nil)
				require.NoError(t, err)
				request.Header.Set("Proxy-Status", "AgentGateway; error=spoof-canary")
				request.Header.Set(contract.HTTPProxyCorrelationHeader, "spoof-canary")
				switch protocol {
				case "plain":
					request.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
					response, err = f.client(t).Do(request)
				case "http/1.1":
					conn := f.intercept(t, upstream.URL, protocol)
					require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
					require.NoError(t, request.Write(conn))
					response, err = http.ReadResponse(bufio.NewReader(conn), request)
				case "h2":
					conn := f.intercept(t, upstream.URL, protocol)
					transport := &http2.Transport{}
					client, clientErr := transport.NewClientConn(conn)
					require.NoError(t, clientErr)
					t.Cleanup(func() { _ = client.Close() })
					response, err = client.RoundTrip(request)
				}
				require.NoError(t, err)
				body, bodyErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				require.Equal(t, 403, response.StatusCode)
				require.Empty(t, response.Header.Get("Proxy-Status"))
				require.Empty(t, response.Header.Get(contract.HTTPProxyCorrelationHeader))
				require.NotContains(t, string(body), "Bad Gateway")
				if interrupted {
					require.Error(t, bodyErr)
				} else {
					require.NoError(t, bodyErr)
					require.Equal(t, "upstream application response", string(body))
				}
				require.EqualValues(t, 1, calls.Load())
				require.Eventually(t, func() bool { f.engine.mu.Lock(); defer f.engine.mu.Unlock(); return f.engine.streams == 0 }, time.Second, time.Millisecond)
				require.True(t, observer.Finish(nil))
				require.NotContains(t, output.String(), "canary")
				if interrupted {
					require.Contains(t, output.String(), `"event":"http_proxy_failure"`)
					require.Contains(t, output.String(), `"stage":"upstream_read"`)
				}
			})
		}
	}
}

func TestIntegrationConnectFailureAndHandshakeWire(t *testing.T) {
	for _, mode := range []string{"dial", "timeout", "handshake"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			target := "127.0.0.1:1"
			var dials atomic.Int64
			f.engine.options.Remote = remote.New(remote.Options{DialContext: func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				if mode == "timeout" {
					return nil, context.DeadlineExceeded
				}
				return nil, errors.New("connect: network unreachable Authorization: Bearer " + f.credential.Bearer)
			}})
			if mode != "handshake" {
				f.allow(t, "http://"+target, "allow_tunnel", "", "")
			}
			var output bytes.Buffer
			observer := diagnostics.New(&output, diagnostics.Warn)
			f.engine.options.Diagnostics = observer
			t.Cleanup(func() { observer.Finish(nil); <-observer.Done() })
			conn, err := net.DialTimeout("tcp", f.address, time.Second)
			require.NoError(t, err)
			defer func() { _ = conn.Close() }()
			require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
			_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", target, target, f.credential.Bearer)
			require.NoError(t, err)
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
			require.NoError(t, err)
			if mode != "handshake" {
				status := 502
				if mode == "timeout" {
					status = 504
				}
				assertFailureResponse(t, response, status)
				require.EqualValues(t, 1, dials.Load())
			} else {
				require.Equal(t, 200, response.StatusCode)
				require.Regexp(t, "^[0-9a-f]{32}$", response.Header.Get(contract.HTTPProxyConnectionHeader))
				_, err = io.WriteString(conn, "not TLS private-canary\r\n")
				require.NoError(t, err)
				trailing, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.NotContains(t, string(trailing), "HTTP/1.1")
				require.Zero(t, dials.Load())
			}
			require.Eventually(t, func() bool { f.engine.mu.Lock(); defer f.engine.mu.Unlock(); return f.engine.work == 0 }, time.Second, time.Millisecond)
			require.True(t, observer.Finish(nil))
			require.NotContains(t, output.String(), "canary")
			require.NotContains(t, output.String(), f.credential.Bearer)
			if mode == "dial" {
				require.Contains(t, output.String(), "connect: network unreachable")
				require.Contains(t, output.String(), target)
			}
			if mode == "handshake" {
				require.Contains(t, output.String(), `"stage":"intercept_handshake"`)
				require.Contains(t, output.String(), response.Header.Get(contract.HTTPProxyConnectionHeader))
			}
		})
	}
}

type proxyDiagnosticSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	failed  bool
}

func (s *proxyDiagnosticSink) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	if s.failed {
		return 0, io.ErrClosedPipe
	}
	<-s.release
	return len(p), nil
}

func TestIntegrationDiagnosticLossDoesNotGateForwarding(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			f := fixture(t)
			sink := &proxyDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{}), failed: failed}
			observer := diagnostics.New(sink, diagnostics.Warn)
			f.engine.options.Diagnostics = observer
			t.Cleanup(func() { close(sink.release); observer.Finish(nil); <-observer.Done() })
			fact := diagnostics.Facts{Event: diagnostics.HTTPProxyFailure, Stage: diagnostics.ProxyHandshake, Cause: diagnostics.Unavailable}
			observer.HTTPProxy(fact)
			select {
			case <-sink.entered:
			case <-time.After(time.Second):
				t.Fatal("sink not entered")
			}
			for range contract.DiagnosticQueueRecords + 10 {
				observer.HTTPProxy(fact)
			}
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = io.WriteString(w, "forwarded") }))
			defer upstream.Close()
			f.allow(t, upstream.URL, "allow_requests", "", "")
			response := f.request(t, f.client(t), "GET", upstream.URL, nil)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, 200, response.StatusCode)
			require.Equal(t, "forwarded", string(body))
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestIntegrationWorkCapacityFailureWire(t *testing.T) {
	for _, principal := range []bool{false, true} {
		t.Run(fmt.Sprint(principal), func(t *testing.T) {
			f := fixture(t)
			f.engine.mu.Lock()
			if principal {
				f.engine.principals[f.credential.Principal.ID] = contract.HTTPProxyPrincipalWork
			} else {
				f.engine.work = contract.HTTPProxyWork
			}
			f.engine.mu.Unlock()
			defer func() {
				f.engine.mu.Lock()
				defer f.engine.mu.Unlock()
				if principal {
					delete(f.engine.principals, f.credential.Principal.ID)
				} else {
					f.engine.work = 0
				}
			}()
			response := f.request(t, f.client(t), "GET", "http://127.0.0.1:1/", nil)
			status := 503
			if principal {
				status = 429
			}
			require.Equal(t, status, response.StatusCode)
			require.Equal(t, "AgentGateway; error=connection_limit_reached", response.Header.Get("Proxy-Status"))
			require.Regexp(t, "^[0-9a-f]{32}$", response.Header.Get(contract.HTTPProxyCorrelationHeader))
			_, err := io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			if principal {
				require.Eventually(t, func() bool { f.engine.mu.Lock(); defer f.engine.mu.Unlock(); return f.engine.work == 0 }, time.Second, time.Millisecond)
			}
		})
	}
}

func TestIntegrationConnectionCapacityClosesSocket(t *testing.T) {
	f := fixture(t)
	var output bytes.Buffer
	observer := diagnostics.New(&output, diagnostics.Warn)
	f.engine.options.Diagnostics = observer
	t.Cleanup(func() { observer.Finish(nil); <-observer.Done() })
	// Reserve the owning map's full capacity without creating 256 unrelated peers.
	f.engine.mu.Lock()
	placeholders := make([]net.Conn, contract.HTTPProxyConnections)
	for i := range placeholders {
		placeholders[i] = &boundConn{}
		f.engine.connections[placeholders[i]] = struct{}{}
	}
	f.engine.mu.Unlock()
	defer func() {
		f.engine.mu.Lock()
		defer f.engine.mu.Unlock()
		for _, c := range placeholders {
			delete(f.engine.connections, c)
		}
	}()
	conn, err := net.DialTimeout("tcp", f.address, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	b := make([]byte, 1)
	n, err := conn.Read(b)
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
	require.True(t, observer.Finish(nil))
	require.Contains(t, output.String(), `"stage":"connection_capacity"`)
	require.Contains(t, output.String(), `"cause":"capacity"`)
}
