//go:build e2e

package e2e_test

// TestDashboardLiveFeed is the M5 SSE live-feed acceptance gate (Task 38).
//
// Scenario:
//  1. Subscribe to /dashboard/api/events.
//  2. Fire 20 requests through the proxy.
//  3. Assert 20 "request" SSE events arrive on the stream.
//  4. Fetch /dashboard/api/audit?limit=100 → 20 rows.

// TestApprovalViewInvariant is the M5 approval-view acceptance gate (Task 38).
//
// Scenario:
//  1. Write a require-approval rule.
//  2. Agent fires POST with body "secret-body" and header "X-Hint: confidential".
//  3. Collect the SSE "approval" event; assert it contains neither "secret-body"
//     nor "X-Hint" / "confidential".
//  4. Fetch /dashboard/api/pending; same assertion.
//  5. Approve via /dashboard/api/decide; request completes.

// TestAgentCancelPropagatesToUpstream is the M5 cancel-propagation gate (Task 38).
//
// Scenario:
//  1. Start a slow upstream that blocks on reading the request body.
//  2. Agent fires a request then cancels the context mid-read.
//  3. Assert the upstream handler sees ctx.Done().

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// adminToken reads the admin token written by the daemon for a given stack.
func adminToken(t *testing.T, stack *TestStack) string {
	t.Helper()
	tokenPath := filepath.Join(stack.CfgHome, "agent-gateway", "admin-token")
	// Retry briefly: daemon writes the token file during startup but may not
	// have flushed it by the time we read (teststack waits for HTTP but not
	// for the token file write).
	var data []byte
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(tokenPath)
		if err == nil && len(bytes.TrimSpace(data)) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read admin token: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// dashGet performs an authenticated GET to the dashboard.
func dashGet(t *testing.T, stack *TestStack, token, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+stack.DashboardAddr+path, nil)
	if err != nil {
		t.Fatalf("build dash GET %s: %v", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("dash GET %s: %v", path, err)
	}
	return resp
}

// subscribeSSE opens a streaming GET to /dashboard/api/events and returns a
// channel that receives parsed SSE event kinds. The goroutine runs until ctx
// is cancelled or the connection closes.
func subscribeSSE(ctx context.Context, t *testing.T, stack *TestStack, token string) <-chan sseEvent {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+stack.DashboardAddr+"/dashboard/api/events", nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE connect: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("SSE response: %d", resp.StatusCode)
	}

	ch := make(chan sseEvent, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if ev.Kind != "" {
					select {
					case ch <- ev:
					default:
					}
				}
				ev = sseEvent{}
			}
		}
	}()
	return ch
}

// sseEvent is a parsed SSE event with its kind and raw JSON data.
type sseEvent struct {
	Kind string
	Data string
}

// collectSSEEvents drains ch until n events of the given kind are collected
// or the deadline elapses. Returns the collected events (may be fewer than n
// if the deadline fires first).
func collectSSEEvents(ch <-chan sseEvent, kind string, n int, deadline time.Duration) []sseEvent {
	var out []sseEvent
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			if ev.Kind == kind {
				out = append(out, ev)
			}
		case <-timer.C:
			return out
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────

func TestDashboardLiveFeed(t *testing.T) {
	const requestCount = 20

	// 1. Start stack with a simple echo upstream.
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))

	token := adminToken(t, stack)

	// 2. Subscribe to SSE before firing requests so we don't miss events.
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()
	events := subscribeSSE(sseCtx, t, stack, token)

	// Wait for the keepalive comment that signals the SSE stream is live.
	// The subscription goroutine filters out comment lines (they don't have
	// "event:" prefixes), so we just wait a short moment after connect.
	time.Sleep(200 * time.Millisecond)

	// 3. Fire 20 requests through the proxy.
	for i := 0; i < requestCount; i++ {
		resp, err := stack.AgentClient.Get(stack.UpstreamURL)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: unexpected status %d", i, resp.StatusCode)
		}
	}

	// 4. Collect 20 "request" SSE events (allow up to 10s).
	got := collectSSEEvents(events, "request", requestCount, 10*time.Second)
	if len(got) < requestCount {
		t.Errorf("SSE: got %d request events, want %d", len(got), requestCount)
	}

	// 5. Fetch audit log; expect 20 rows.
	auditResp := dashGet(t, stack, token, fmt.Sprintf("/dashboard/api/audit?limit=%d", requestCount+10))
	defer auditResp.Body.Close()
	if auditResp.StatusCode != http.StatusOK {
		t.Fatalf("audit: unexpected status %d", auditResp.StatusCode)
	}
	var auditBody struct {
		Records []json.RawMessage `json:"records"`
		Total   int               `json:"total"`
	}
	if err := json.NewDecoder(auditResp.Body).Decode(&auditBody); err != nil {
		t.Fatalf("audit: decode: %v", err)
	}
	if auditBody.Total < requestCount {
		t.Errorf("audit: total=%d, want >= %d", auditBody.Total, requestCount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────

func TestApprovalViewInvariant(t *testing.T) {
	// 1. Start stack with a simple upstream.
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "approved")
	}))

	token := adminToken(t, stack)

	// Extract upstream host for the rule match.
	upURL, err := url.Parse(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	upstreamHost := upURL.Hostname()

	// 2. Write a require-approval rule (no header assertions → empty
	// assertedHeaders → approval view has no headers at all).
	ruleContent := fmt.Sprintf(`
rule "require-all" {
  verdict = "require-approval"
  match {
    host = %q
  }
}
`, upstreamHost)
	stack.writeRule(t, "require-all.hcl", ruleContent)
	stack.rulesReload(t)
	time.Sleep(200 * time.Millisecond)

	// 3. Subscribe to SSE.
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()
	events := subscribeSSE(sseCtx, t, stack, token)
	time.Sleep(200 * time.Millisecond)

	// 4. Agent fires a POST with a sensitive body and header in a background
	// goroutine; the proxy blocks waiting for approval.
	type agentResult struct {
		status int
		err    error
	}
	agentDone := make(chan agentResult, 1)
	go func() {
		body := strings.NewReader("secret-body")
		req, e := http.NewRequest(http.MethodPost, stack.UpstreamURL, body)
		if e != nil {
			agentDone <- agentResult{err: e}
			return
		}
		req.Header.Set("X-Hint", "confidential")
		resp, e := stack.AgentClient.Do(req)
		if e != nil {
			agentDone <- agentResult{err: e}
			return
		}
		resp.Body.Close()
		agentDone <- agentResult{status: resp.StatusCode}
	}()

	// 5. Collect the SSE "approval" event.
	approvalEvents := collectSSEEvents(events, "approval", 1, 10*time.Second)
	if len(approvalEvents) == 0 {
		t.Fatal("no approval SSE event received within 10s")
	}
	sseData := approvalEvents[0].Data

	// Assert the SSE approval payload does NOT leak sensitive data.
	if strings.Contains(sseData, "secret-body") {
		t.Errorf("SSE approval event contains 'secret-body': %s", sseData)
	}
	if strings.Contains(strings.ToLower(sseData), "x-hint") {
		t.Errorf("SSE approval event contains 'X-Hint': %s", sseData)
	}
	if strings.Contains(strings.ToLower(sseData), "confidential") {
		t.Errorf("SSE approval event contains 'confidential': %s", sseData)
	}

	// 6. Fetch /api/pending and assert the same invariant.
	pendingResp := dashGet(t, stack, token, "/dashboard/api/pending")
	defer pendingResp.Body.Close()
	if pendingResp.StatusCode != http.StatusOK {
		t.Fatalf("pending: unexpected status %d", pendingResp.StatusCode)
	}
	pendingBody, err := io.ReadAll(pendingResp.Body)
	if err != nil {
		t.Fatalf("pending: read body: %v", err)
	}
	pendingStr := string(pendingBody)

	if strings.Contains(pendingStr, "secret-body") {
		t.Errorf("/api/pending contains 'secret-body': %s", pendingStr)
	}
	if strings.Contains(strings.ToLower(pendingStr), "x-hint") {
		t.Errorf("/api/pending contains 'X-Hint': %s", pendingStr)
	}
	if strings.Contains(strings.ToLower(pendingStr), "confidential") {
		t.Errorf("/api/pending contains 'confidential': %s", pendingStr)
	}

	// 7. Extract the request ID from the SSE event so we can approve it.
	var approvalPayload struct {
		RequestID string `json:"request_id"`
	}
	// The SSE data is a full ApprovalRequest (JSON). Extract RequestID.
	var approvalReq struct {
		RequestID string `json:"RequestID"`
	}
	if err := json.Unmarshal([]byte(sseData), &approvalReq); err != nil {
		t.Fatalf("parse approval SSE data: %v", err)
	}
	approvalPayload.RequestID = approvalReq.RequestID
	if approvalPayload.RequestID == "" {
		t.Fatal("approval SSE event has empty RequestID")
	}

	// 8. Approve the request via /api/decide.
	decidePayload := fmt.Sprintf(`{"id":%q,"decision":"approve"}`, approvalPayload.RequestID)
	decideReq, err := http.NewRequest(http.MethodPost,
		"http://"+stack.DashboardAddr+"/dashboard/api/decide",
		strings.NewReader(decidePayload))
	if err != nil {
		t.Fatalf("build decide request: %v", err)
	}
	decideReq.Header.Set("Authorization", "Bearer "+token)
	decideReq.Header.Set("Content-Type", "application/json")
	decideResp, err := http.DefaultClient.Do(decideReq)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	decideResp.Body.Close()
	if decideResp.StatusCode != http.StatusOK {
		t.Fatalf("decide: unexpected status %d", decideResp.StatusCode)
	}

	// 9. Await the agent request completing with 200.
	select {
	case result := <-agentDone:
		if result.err != nil {
			t.Fatalf("agent request error after approval: %v", result.err)
		}
		if result.status != http.StatusOK {
			t.Errorf("agent request: got status %d, want 200", result.status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent request did not complete within 10s after approval")
	}
}

// ─────────────────────────────────────────────────────────────────────────────

func TestAgentCancelPropagatesToUpstream(t *testing.T) {
	// ctxCancelled is set to 1 atomically when the upstream observes ctx.Done().
	var ctxCancelled atomic.Int32
	// bodyReadStarted is closed when the upstream starts reading the body,
	// so the test can cancel the agent at exactly the right moment.
	bodyReadStarted := make(chan struct{})

	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Signal that we've started trying to read the body.
		close(bodyReadStarted)

		// Block reading from the body until the context is cancelled.
		buf := make([]byte, 1)
		for {
			select {
			case <-r.Context().Done():
				ctxCancelled.Store(1)
				return
			default:
			}
			_, err := r.Body.Read(buf)
			if err != nil {
				// EOF or connection reset — check context.
				if r.Context().Err() != nil {
					ctxCancelled.Store(1)
				}
				return
			}
		}
	}))

	// Build an agent request with a body that will keep the upstream reading.
	// We use a pipe so we control when bytes arrive.
	pr, pw := io.Pipe()

	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()

	req, err := http.NewRequestWithContext(agentCtx, http.MethodPost, stack.UpstreamURL, pr)
	if err != nil {
		t.Fatalf("build agent request: %v", err)
	}

	// Fire request in background.
	go func() {
		resp, _ := stack.AgentClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		pw.Close()
	}()

	// Write one byte to unblock the proxy's request forwarding, then wait for
	// the upstream to start reading.
	if _, err := pw.Write([]byte("x")); err != nil {
		t.Logf("pipe write: %v (may be ok if upstream already cancelled)", err)
	}

	select {
	case <-bodyReadStarted:
		// Upstream is reading; cancel the agent now.
	case <-time.After(10 * time.Second):
		t.Fatal("upstream did not start reading body within 10s")
	}

	agentCancel()

	// Give the cancellation time to propagate through the proxy to the upstream.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ctxCancelled.Load() == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if ctxCancelled.Load() != 1 {
		t.Error("upstream context was NOT cancelled after agent cancellation")
	}
}
