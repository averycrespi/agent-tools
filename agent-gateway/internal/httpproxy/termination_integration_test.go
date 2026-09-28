//go:build integration

package httpproxy

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

type terminationWriter struct {
	*httptest.ResponseRecorder
	stage  string
	calls  int
	cancel context.CancelFunc
}

func (w *terminationWriter) EnableFullDuplex() error { return nil }
func (w *terminationWriter) SetWriteDeadline(time.Time) error {
	if w.stage == "deadline" {
		return errors.New("secret deadline error")
	}
	return nil
}
func (w *terminationWriter) FlushError() error {
	w.calls++
	if w.stage == "downstream_flush" && w.calls > 1 {
		return errors.New("secret flush error")
	}
	return nil
}
func (w *terminationWriter) Write(b []byte) (int, error) {
	if w.stage == "downstream_write" {
		if w.cancel != nil {
			w.cancel()
		}
		return 0, errors.New("secret write error")
	}
	return w.ResponseRecorder.Write(b)
}

func assertTermination(t *testing.T, f *proxyFixture, expected contract.HTTPTermination, outcome string) {
	t.Helper()
	reader, err := invocation.NewReadService(f.engine.options.Evidence, f.authority)
	require.NoError(t, err)
	var item contract.HTTPTrafficSummary
	require.Eventually(t, func() bool {
		page, e := reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 10})
		if e != nil {
			return false
		}
		for _, row := range page.Items {
			if row.Type == "request" && row.CompletionRecorded {
				item = row
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, outcome, item.Outcome)
	require.Equal(t, &expected, item.Termination)
	record, err := reader.GetHTTP(t.Context(), item.ID)
	require.NoError(t, err)
	require.Equal(t, &expected, record.Completion.Termination)
	require.Equal(t, outcome, record.Completion.Outcome)
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
	require.NotContains(t, string(raw), "response.completed")
	completion, err := json.Marshal(record.Completion)
	require.NoError(t, err)
	require.LessOrEqual(t, len(completion), contract.HTTPTrafficCompletionBytes)
}

func TestIntegrationTransferOperationFailuresPersist(t *testing.T) {
	for _, stage := range []string{"deadline", "downstream_write", "downstream_flush"} {
		t.Run(stage, func(t *testing.T) {
			f := fixture(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "data: response.completed secret\n\n")
			}))
			defer upstream.Close()
			f.allow(t, upstream.URL, "allow_requests", "", "")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := httptest.NewRequest("GET", upstream.URL, nil).WithContext(ctx)
			req.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
			writer := &terminationWriter{ResponseRecorder: httptest.NewRecorder(), stage: stage, cancel: cancel}
			require.PanicsWithValue(t, http.ErrAbortHandler, func() { f.engine.handle(writer, req, nil) })
			observed := contract.HTTPTermination{Stage: stage, Condition: "failure"}
			if stage == "downstream_write" {
				observed.Context = "cancelled"
			}
			assertTermination(t, f, observed, "outcome_unknown")
		})
	}
}

func TestIntegrationTerminalLikeEventBeforeEOFCancellation(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2"} {
		for _, drain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/drain=%t", protocol, drain), func(t *testing.T) {
				f := fixture(t)
				var calls atomic.Int64
				releaseUpstream := make(chan struct{})
				const event = "data: {\"type\":\"response.completed\"}\n\n"
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					// Consume the empty H2 POST's chunked upstream framing so net/http
					// can observe peer closure while the response handler is blocked.
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, event)
					_ = http.NewResponseController(w).Flush()
					// Do not let upstream cancellation return a normal chunked EOF
					// that can race Gateway's canceled read into clean completion.
					// The test releases finalization only after retained evidence.
					select {
					case <-releaseUpstream:
					case <-time.After(5 * time.Second):
						t.Error("upstream evidence barrier expired")
						panic(http.ErrAbortHandler)
					}
				}))
				defer upstream.Close()
				defer close(releaseUpstream)
				f.engine.roots = x509.NewCertPool()
				f.engine.roots.AddCert(upstream.Certificate())
				f.allow(t, upstream.URL, "allow_requests", "", "")
				conn := f.intercept(t, upstream.URL, protocol)
				require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
				var response *http.Response
				var err error
				if protocol == "h2" {
					cc, e := (&http2.Transport{}).NewClientConn(conn)
					require.NoError(t, e)
					defer func() { _ = cc.Close() }()
					req, e := http.NewRequestWithContext(t.Context(), "POST", upstream.URL, nil)
					require.NoError(t, e)
					response, err = cc.RoundTrip(req)
				} else {
					u, e := url.Parse(upstream.URL)
					require.NoError(t, e)
					_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: %s\r\nContent-Length: 0\r\n\r\n", u.Host)
					require.NoError(t, err)
					response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
				}
				require.NoError(t, err)
				got := make([]byte, len(event))
				_, err = io.ReadFull(response.Body, got)
				require.NoError(t, err)
				require.Equal(t, event, string(got))
				require.Equal(t, 200, response.StatusCode)
				if drain {
					f.engine.BeginDrain()
				} else {
					require.NoError(t, conn.Close())
				}
				_ = response.Body.Close()
				assertTermination(t, f, contract.HTTPTermination{Stage: "upstream_read", Condition: "cancelled", Context: "cancelled"}, "outcome_unknown")
				require.EqualValues(t, 1, calls.Load())
			})
		}
	}
}

func TestIntegrationTransferContextDeadlinePersists(t *testing.T) {
	f := fixture(t)
	releaseUpstream := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: response.completed\n\n")
		_ = http.NewResponseController(w).Flush()
		select {
		case <-releaseUpstream:
		case <-time.After(5 * time.Second):
			t.Error("upstream evidence barrier expired")
			panic(http.ErrAbortHandler)
		}
	}))
	defer upstream.Close()
	defer close(releaseUpstream)
	f.allow(t, upstream.URL, "allow_requests", "", "")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest("GET", upstream.URL, nil).WithContext(ctx)
	req.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
	writer := &terminationWriter{ResponseRecorder: httptest.NewRecorder()}
	require.PanicsWithValue(t, http.ErrAbortHandler, func() { f.engine.handle(writer, req, nil) })
	require.Contains(t, writer.Body.String(), "response.completed")
	assertTermination(t, f, contract.HTTPTermination{Stage: "upstream_read", Condition: "timeout", Context: "timeout"}, "outcome_unknown")
}

func TestTransferConditionsKeepErrorAndContextSeparate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		err       error
		condition string
	}{
		{io.ErrUnexpectedEOF, "failure"}, {context.Canceled, "cancelled"}, {context.DeadlineExceeded, "timeout"},
	} {
		require.Equal(t, &contract.HTTPTermination{Stage: "upstream_read", Condition: tc.condition, Context: "cancelled"}, termination(ctx, "upstream_read", tc.err))
	}
	require.Equal(t, &contract.HTTPTermination{Stage: "downstream_write", Condition: "failure", Context: "cancelled"}, termination(ctx, "upstream_read", transferFailure("downstream_write", errors.New(strings.Repeat("secret", 100)))))
}
