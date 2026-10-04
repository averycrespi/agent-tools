//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func readBodylessH2(t *testing.T, conn net.Conn, upstream string) *http.Response {
	t.Helper()
	u, err := url.Parse(upstream)
	require.NoError(t, err)
	_, err = io.WriteString(conn, http2.ClientPreface)
	require.NoError(t, err)
	framer := http2.NewFramer(conn, conn)
	framer.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	framer.MaxHeaderListSize = 32 << 10
	require.NoError(t, framer.WriteSettings())
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	for _, field := range []hpack.HeaderField{
		{Name: ":method", Value: "GET"},
		{Name: ":scheme", Value: "https"},
		{Name: ":authority", Value: u.Host},
		{Name: ":path", Value: "/"},
	} {
		require.NoError(t, encoder.WriteField(field))
	}
	require.NoError(t, framer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block.Bytes(), EndHeaders: true, EndStream: true}))
	response := &http.Response{Header: make(http.Header), Body: http.NoBody}
	for range 32 {
		frame, err := framer.ReadFrame()
		require.NoError(t, err)
		ended := false
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if !frame.IsAck() {
				require.NoError(t, framer.WriteSettingsAck())
			}
		case *http2.MetaHeadersFrame:
			require.EqualValues(t, 1, frame.StreamID)
			require.False(t, frame.Truncated)
			require.Zero(t, response.StatusCode, "unexpected trailers")
			for _, field := range frame.Fields {
				if field.Name == ":status" {
					require.Equal(t, "304", field.Value)
					response.StatusCode = http.StatusNotModified
				} else {
					response.Header.Add(field.Name, field.Value)
				}
			}
			ended = frame.StreamEnded()
		case *http2.DataFrame:
			require.EqualValues(t, 1, frame.StreamID)
			require.Empty(t, frame.Data(), "304 must carry no payload")
			ended = frame.StreamEnded()
		case *http2.RSTStreamFrame, *http2.GoAwayFrame:
			t.Fatalf("unexpected stream failure: %v", frame)
		}
		if ended {
			require.Equal(t, http.StatusNotModified, response.StatusCode)
			return response
		}
	}
	t.Fatal("response did not end within frame bound")
	return nil
}

func TestIntegrationInterceptBodylessResponses(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2"} {
		for _, tc := range []struct {
			name, method string
			status       int
			length, body string
			interrupted  bool
		}{
			{"no_content", "GET", 204, "", "", false},
			{"head", "HEAD", 200, "42", "", false},
			{"not_modified", "GET", 304, "42", "", false},
			{"empty_ok", "GET", 200, "0", "", false},
			{"body_ok", "GET", 200, "7", "payload", false},
			{"truncated_body", "GET", 200, "42", "payload", true},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				f := fixture(t)
				var calls atomic.Int64
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != tc.method {
						t.Errorf("method = %s", r.Method)
					}
					if tc.status == http.StatusNotModified {
						// net/http's server strips 304 Content-Length; send the
						// legal representation metadata on the actual upstream wire.
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = conn.Close() }()
						_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
						_, err = fmt.Fprintf(conn, "HTTP/1.1 304 Not Modified\r\nContent-Length: %s\r\nETag: \"fixture\"\r\nConnection: close\r\n\r\n", tc.length)
						if err != nil {
							t.Error(err)
						}
						return
					}
					w.Header().Set("Proxy-Status", "AgentGateway; error=private-spoof-canary")
					w.Header().Set(contract.HTTPProxyCorrelationHeader, "private-spoof-canary")
					w.Header().Set("ETag", `"fixture"`)
					if tc.length != "" {
						w.Header().Set("Content-Length", tc.length)
					}
					w.WriteHeader(tc.status)
					if tc.body != "" {
						_, _ = io.WriteString(w, tc.body)
					}
				}))
				defer upstream.Close()
				f.engine.roots = x509.NewCertPool()
				f.engine.roots.AddCert(upstream.Certificate())
				f.allow(t, upstream.URL, "allow_requests", "", "")
				conn := f.intercept(t, upstream.URL, protocol)
				require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
				var response *http.Response
				var err error
				if protocol == "h2" && tc.status == http.StatusNotModified {
					// x/net's client treats 304 representation length as missing
					// payload. Assert the actual headers and END_STREAM instead.
					response = readBodylessH2(t, conn, upstream.URL)
				} else if protocol == "h2" {
					cc, e := (&http2.Transport{}).NewClientConn(conn)
					require.NoError(t, e)
					defer func() { _ = cc.Close() }()
					req, e := http.NewRequestWithContext(t.Context(), tc.method, upstream.URL+"/", nil)
					require.NoError(t, e)
					response, err = cc.RoundTrip(req)
				} else {
					u, e := url.Parse(upstream.URL)
					require.NoError(t, e)
					_, err = fmt.Fprintf(conn, "%s / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", tc.method, u.Host)
					require.NoError(t, err)
					response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: tc.method})
				}
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				if tc.interrupted {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.NoError(t, response.Body.Close())
				require.Empty(t, response.Header.Get("Proxy-Status"))
				require.Empty(t, response.Header.Get(contract.HTTPProxyCorrelationHeader))
				require.Equal(t, tc.status, response.StatusCode)
				require.Equal(t, tc.body, string(body))
				require.Equal(t, `"fixture"`, response.Header.Get("ETag"))
				length := tc.length
				if protocol == "http/1.1" && tc.status == http.StatusNotModified {
					length = "" // Preserve net/http's existing H1 suppression.
				}
				require.Equal(t, length, response.Header.Get("Content-Length"))
				require.Eventually(t, func() bool {
					history, e := f.traffic.HTTPHistory(t.Context(), 0, 10)
					if e != nil {
						return false
					}
					for _, record := range history.Records {
						if record.Completion != nil && record.Completion.ResponseSource == "upstream" {
							outcome := "succeeded"
							if tc.interrupted {
								outcome = "outcome_unknown"
							}
							return record.Completion.Outcome == outcome && record.Completion.Status == tc.status && record.Completion.BytesReceived == int64(len(tc.body)) && record.Completion.GatewayStatus == 0
						}
					}
					return false
				}, 3*time.Second, 10*time.Millisecond)
				observed := contract.HTTPTermination{Stage: "complete", Condition: "clean"}
				outcome := "succeeded"
				if tc.method == "HEAD" {
					observed.Stage = "response_headers"
				}
				if tc.interrupted {
					observed = contract.HTTPTermination{Stage: "upstream_read", Condition: "failure"}
					outcome = "outcome_unknown"
				}
				assertTermination(t, f, observed, outcome)
				require.EqualValues(t, 1, calls.Load())
			})
		}
	}
}
