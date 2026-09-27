//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestIntegrationRejectionDiagnosticsPersistAndRead(t *testing.T) {
	f := fixture(t)
	reader, err := invocation.NewReadService(f.engine.options.Evidence, f.authority)
	require.NoError(t, err)
	tests := []struct {
		stage, reason string
		modify        func(*http.Request)
	}{
		{"headers", "invalid_headers", func(r *http.Request) { r.Header.Set("X-Test", strings.Repeat("header-secret", 1000)) }},
		{"headers", "trailers_unsupported", func(r *http.Request) { r.Trailer = http.Header{"X-Secret": {"trailer-secret"}} }},
		{"headers", "upgrade_unsupported", func(r *http.Request) { r.Header.Set("Upgrade", "upgrade-secret") }},
		{"request_form", "connect_body", func(r *http.Request) { r.Method = "CONNECT"; r.ContentLength = 1 }},
		{"request_form", "absolute_http_required", func(r *http.Request) { r.URL.Scheme = "https" }},
		{"target", "invalid_request_target", func(r *http.Request) { r.RequestURI = "http://example.com/%2fpath-secret?query-secret" }},
		{"target", "invalid_connect_target", func(r *http.Request) { r.Method = "CONNECT"; r.RequestURI = "host-secret/path-secret" }},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://example.com/path-secret?query-secret", strings.NewReader("body-secret"))
			req.ContentLength = 0
			req.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
			req.Header.Set("Authorization", "authorization-secret")
			tt.modify(req)
			response := httptest.NewRecorder()
			f.engine.handle(response, req, nil)
			require.Equal(t, 400, response.Code)
			page, err := reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 1})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			summary := page.Items[0]
			require.Equal(t, &contract.HTTPRejection{Stage: tt.stage, Reason: tt.reason}, summary.Rejection)
			require.Equal(t, "gateway", summary.ResponseSource)
			record, err := reader.GetHTTP(t.Context(), summary.ID)
			require.NoError(t, err)
			require.Equal(t, summary.Rejection, record.Admission.Rejection)
			require.Nil(t, record.Admission.Target)
			require.Nil(t, record.Admission.Connect)
			require.Nil(t, record.Completion)
			raw, err := json.Marshal(record)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "secret")
			require.NotContains(t, string(raw), f.credential.Bearer)
			diagnostic, err := json.Marshal(record.Admission.Rejection)
			require.NoError(t, err)
			require.LessOrEqual(t, len(diagnostic), 128)
		})
	}
}

func TestIntegrationGatewayResponseIsNotUpstreamStatus(t *testing.T) {
	f := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	f.allow(t, upstream.URL, "allow_requests", "", "")
	upstream.Close()
	response := f.request(t, f.client(t), "GET", upstream.URL, nil)
	require.Equal(t, 502, response.StatusCode)
	_, err := io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Eventually(t, func() bool {
		history, err := f.traffic.HTTPHistory(t.Context(), 0, 10)
		return err == nil && len(history.Records) == 1 && history.Records[0].Completion != nil
	}, 3*time.Second, 10*time.Millisecond)
	reader, err := invocation.NewReadService(f.engine.options.Evidence, f.authority)
	require.NoError(t, err)
	page, err := reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "gateway", page.Items[0].ResponseSource)
	record, err := reader.GetHTTP(t.Context(), page.Items[0].ID)
	require.NoError(t, err)
	require.Equal(t, "outcome_unknown", record.Completion.Outcome)
	require.Zero(t, record.Completion.Status)
	require.Equal(t, 502, record.Completion.GatewayStatus)
}

func TestIntegrationInnerRequestFormDiagnostics(t *testing.T) {
	f := fixture(t)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("rejected request reached upstream") }))
	defer upstream.Close()
	for _, tt := range []struct{ line, header, reason string }{
		{"CONNECT example.com:443", "", "nested_connect"},
		{"GET https://example.com/path-secret", "", "origin_form_required"},
		{"GET /path-secret", "Proxy-Authorization: inner-secret\r\n", "inner_proxy_authorization"},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			conn := f.intercept(t, upstream.URL, "http/1.1")
			_, err := fmt.Fprintf(conn, "%s HTTP/1.1\r\nHost: example.com\r\n%s\r\n", tt.line, tt.header)
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
			require.NoError(t, err)
			require.Equal(t, 400, response.StatusCode)
			require.NoError(t, response.Body.Close())
			history, err := f.traffic.HTTPHistory(t.Context(), 0, 100)
			require.NoError(t, err)
			inner := history.Records[len(history.Records)-1].Admission
			parent := history.Records[len(history.Records)-2].Admission
			require.Equal(t, tt.reason, inner.Rejection.Reason)
			require.NotNil(t, inner.Connect)
			require.Equal(t, parent.ID, inner.Connect.ID)
			require.Equal(t, parent.Target.Host, inner.Connect.Host)
			require.Nil(t, inner.Target)
		})
	}
}

func TestIntegrationConcurrentH2RejectionsUseActualConnect(t *testing.T) {
	f := fixture(t)
	// A second identity shares this engine and traffic writer, not a second proxy.
	second := *f
	ctx := audit.WithSystem(t.Context())
	principal, err := f.authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "Second", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	second.credential, err = f.authority.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("rejected request reached upstream") }))
	defer upstream.Close()
	clients := []*proxyFixture{f, f, &second}
	type expected struct {
		parent    string
		principal string
		reason    string
	}
	expectedParents := make(map[string]expected)
	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, 18)
	reasons := []string{"invalid_request_target", "inner_proxy_authorization", "invalid_headers"}
	for index, client := range clients {
		connection := client.intercept(t, upstream.URL, "h2")
		history, err := f.traffic.HTTPHistory(t.Context(), 0, 100)
		require.NoError(t, err)
		parent := history.Records[len(history.Records)-1].Admission
		expectedParents[parent.ID] = expected{parent.ID, client.credential.Principal.ID, reasons[index]}
		transport := &http2.Transport{}
		h2, err := transport.NewClientConn(connection)
		require.NoError(t, err)
		t.Cleanup(func() { _ = h2.Close() })
		for n := 0; n < 6; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				req, err := http.NewRequestWithContext(t.Context(), "GET", upstream.URL+"/%2fpath-secret?query-secret", nil)
				if err != nil {
					failures <- err
					return
				}
				if index == 1 {
					req.Header.Set("Proxy-Authorization", "inner-secret")
				}
				if index == 2 {
					req.Header.Set("X-Test", strings.Repeat("header-secret", 1000))
				}
				response, err := h2.RoundTrip(req)
				if err != nil {
					failures <- err
					return
				}
				_, err = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if err != nil {
					failures <- err
					return
				}
				if response.StatusCode != 400 {
					failures <- io.ErrUnexpectedEOF
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	history, err := f.traffic.HTTPHistory(t.Context(), 0, 100)
	require.NoError(t, err)
	require.Len(t, history.Records, 21)
	counts := make(map[string]int)
	for _, record := range history.Records {
		a := record.Admission
		if a.Class != "invalid_request" {
			continue
		}
		require.NotNil(t, a.Connect)
		parent, ok := expectedParents[a.Connect.ID]
		require.True(t, ok)
		require.Equal(t, parent.principal, a.Principal.ID)
		require.Nil(t, a.Target)
		require.NotNil(t, a.Rejection)
		require.Equal(t, parent.reason, a.Rejection.Reason)
		counts[parent.parent]++
	}
	require.Len(t, counts, 3)
	for _, count := range counts {
		require.Equal(t, 6, count)
	}
	raw, err := json.Marshal(history)
	require.NoError(t, err)
	require.False(t, bytes.Contains(raw, []byte("secret")))
}
