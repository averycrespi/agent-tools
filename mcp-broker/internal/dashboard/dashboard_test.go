package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/grants"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

func approvalRequest(id, tool string, input map[string]any) broker.ApprovalRequest {
	return broker.ApprovalRequest{
		ID: id, OccurredAt: time.Date(2026, 8, 3, 18, 42, 0, 0, time.UTC),
		ToolName: tool, ToolInput: input,
	}
}

type fakeToolLister struct{ tools []server.Tool }

func (f *fakeToolLister) Tools() []server.Tool { return f.tools }

type fakeToolAndBackendLister struct {
	tools    []server.Tool
	backends []server.BackendStatus
}

func (f *fakeToolAndBackendLister) Tools() []server.Tool { return f.tools }

func (f *fakeToolAndBackendLister) BackendStatuses() []server.BackendStatus { return f.backends }

type fakeRulesLister struct{ rules []config.RuleConfig }

func (f *fakeRulesLister) Rules() []config.RuleConfig { return f.rules }

type fakeGrantLister struct{ grants []grants.Grant }

type fakeAuditQuerier struct {
	records []audit.Record
	total   int
}

type captureAuditQuerier struct {
	opts audit.QueryOpts
}

func (f *fakeGrantLister) List(context.Context, time.Time) ([]grants.Grant, error) {
	return f.grants, nil
}

func (f fakeAuditQuerier) Query(context.Context, audit.QueryOpts) ([]audit.Record, int, error) {
	return f.records, f.total, nil
}

func (f *captureAuditQuerier) Query(_ context.Context, opts audit.QueryOpts) ([]audit.Record, int, error) {
	f.opts = opts
	return []audit.Record{}, 0, nil
}

func TestDashboard_GrantsAPIReadOnlyMetadata(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	d := NewWithGrants(nil, nil, nil, &fakeGrantLister{grants: []grants.Grant{{
		ID:          "grant-1",
		Name:        "release",
		Description: "deploy",
		Fingerprint: "abc123def456",
		Status:      "active",
		Rules:       []config.RuleConfig{{Tool: "git.push", Verdict: "allow", Reason: "release"}},
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Hour),
	}}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/grants", nil)
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	require.NotContains(t, body, "token")
	require.NotContains(t, body, "hash")

	var payload struct {
		Grants []grants.Grant `json:"grants"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Len(t, payload.Grants, 1)
	require.Equal(t, "grant-1", payload.Grants[0].ID)
	require.Equal(t, "abc123def456", payload.Grants[0].Fingerprint)
	require.Equal(t, "active", payload.Grants[0].Status)
	require.Equal(t, "git.push", payload.Grants[0].Rules[0].Tool)
}

func TestDashboard_AuditAPIIncludesGrantAttribution(t *testing.T) {
	rec := audit.Record{
		Timestamp:        time.Now().UTC(),
		Tool:             "git.push",
		Verdict:          "allow",
		GrantID:          "grant-1",
		GrantName:        "release",
		GrantFingerprint: "abc123def456",
		GrantStatus:      "active",
		RuleSource:       "grant",
	}
	d := New(nil, nil, fakeAuditQuerier{records: []audit.Record{rec}, total: 1}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var payload struct {
		Records []audit.Record `json:"records"`
		Total   int            `json:"total"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Equal(t, 1, payload.Total)
	require.Len(t, payload.Records, 1)
	require.Equal(t, "grant-1", payload.Records[0].GrantID)
	require.Equal(t, "release", payload.Records[0].GrantName)
	require.Equal(t, "abc123def456", payload.Records[0].GrantFingerprint)
	require.Equal(t, "active", payload.Records[0].GrantStatus)
	require.Equal(t, "grant", payload.Records[0].RuleSource)
}

func TestDashboard_AuditAPIForwardsFilters(t *testing.T) {
	auditor := &captureAuditQuerier{}
	d := New(nil, nil, auditor, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/audit?tool=git&source=fall-through&status=error&verdict=deny&limit=25&offset=50", nil)
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, audit.QueryOpts{
		Tool:    "git",
		Source:  "fall-through",
		Status:  "error",
		Verdict: "deny",
		Limit:   25,
		Offset:  50,
	}, auditor.opts)
}

func TestDashboard_Review_ApprovesViaAPI(t *testing.T) {
	d := New(nil, nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Start a review in a goroutine
	done := make(chan bool, 1)
	go func() {
		approved, _, err := d.Review(context.Background(), approvalRequest("approval-1", "github.push", map[string]any{"branch": "main"}))
		require.NoError(t, err)
		done <- approved
	}()

	// Wait for the pending request to appear
	time.Sleep(50 * time.Millisecond)

	// Get pending requests
	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var pending []pendingRequest
	err = json.NewDecoder(resp.Body).Decode(&pending)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "approval-1", pending[0].ID)
	require.Equal(t, time.Date(2026, 8, 3, 18, 42, 0, 0, time.UTC), pending[0].Timestamp)

	// Approve it
	body := `{"id":"` + pending[0].ID + `","decision":"approve"}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	approved := <-done
	require.True(t, approved)
}

func TestDashboard_Review_DeniesViaAPI(t *testing.T) {
	d := New(nil, nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	type result struct {
		approved bool
		reason   string
	}
	done := make(chan result, 1)
	go func() {
		approved, reason, err := d.Review(context.Background(), approvalRequest("approval-2", "github.push", map[string]any{}))
		require.NoError(t, err)
		done <- result{approved, reason}
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var pending []pendingRequest
	err = json.NewDecoder(resp.Body).Decode(&pending)
	require.NoError(t, err)

	body := `{"id":"` + pending[0].ID + `","decision":"deny"}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	r := <-done
	require.False(t, r.approved)
	require.Equal(t, "user", r.reason)
}

func TestDashboard_Review_DeniesViaAPIWithReason(t *testing.T) {
	d := New(nil, nil, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	type result struct {
		approved bool
		reason   string
	}
	done := make(chan result, 1)
	go func() {
		approved, reason, err := d.Review(context.Background(), approvalRequest("approval-3", "github.push", map[string]any{}))
		require.NoError(t, err)
		done <- result{approved, reason}
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var pending []pendingRequest
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pending))

	body := `{"id":"` + pending[0].ID + `","decision":"deny","reason":"  needs narrower scope  "}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	r := <-done
	require.False(t, r.approved)
	require.Equal(t, "user: needs narrower scope", r.reason)
}

func TestDashboard_Review_DeniesViaAPIWithBlankReason(t *testing.T) {
	d := New(nil, nil, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	done := make(chan string, 1)
	go func() {
		_, reason, err := d.Review(context.Background(), approvalRequest("approval-4", "github.push", map[string]any{}))
		require.NoError(t, err)
		done <- reason
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var pending []pendingRequest
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pending))

	body := `{"id":"` + pending[0].ID + `","decision":"deny","reason":"   "}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	require.Equal(t, "user", <-done)
}

func TestDashboard_Review_CancelsOnContextDone(t *testing.T) {
	d := New(nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		approved bool
		reason   string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		approved, reason, err := d.Review(ctx, approvalRequest("approval-5", "test.tool", nil))
		done <- result{approved, reason, err}
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	r := <-done
	require.NoError(t, r.err)
	require.False(t, r.approved)
	require.Equal(t, "timeout", r.reason)
}

func TestDashboard_PendingRequest_HasDeadline(t *testing.T) {
	d := New(nil, nil, nil, nil)

	deadline := time.Now().Add(10 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = d.Review(ctx, approvalRequest("approval-6", "test.tool", nil))
	}()

	time.Sleep(50 * time.Millisecond)

	d.mu.Lock()
	var pr *pendingRequest
	for _, p := range d.pending {
		pr = p
		break
	}
	d.mu.Unlock()

	require.NotNil(t, pr)
	require.WithinDuration(t, deadline, pr.Deadline, time.Second)

	cancel()
	<-done
}

func TestDashboard_UnauthorizedPage(t *testing.T) {
	d := New(nil, nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/unauthorized")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "Unauthorized")
}

func TestHandleTools_SerializesAnnotationsOutputSchemaAndMeta(t *testing.T) {
	readOnly := true
	tools := &fakeToolLister{tools: []server.Tool{
		{
			Name:        "github.search",
			Description: "Search",
			Annotations: &mcp.ToolAnnotation{Title: "Search", ReadOnlyHint: &readOnly},
			OutputSchema: &mcp.ToolOutputSchema{
				Type:       "object",
				Properties: map[string]any{"hits": map[string]any{"type": "integer"}},
			},
			Meta: &mcp.Meta{AdditionalFields: map[string]any{"trace_id": "abc"}},
		},
		{Name: "fs.write"},
	}}
	d := New(tools, nil, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/tools")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Tools []struct {
			Name         string         `json:"name"`
			Description  string         `json:"description"`
			Annotations  map[string]any `json:"annotations"`
			OutputSchema map[string]any `json:"outputSchema"`
			Meta         map[string]any `json:"_meta"`
		} `json:"tools"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Tools, 2)

	rich := body.Tools[1] // sorted: fs.write < github.search
	require.Equal(t, "github.search", rich.Name)
	require.Equal(t, "Search", rich.Annotations["title"])
	require.Equal(t, true, rich.Annotations["readOnlyHint"])
	require.Equal(t, "object", rich.OutputSchema["type"])
	require.Equal(t, "abc", rich.Meta["trace_id"])

	plain := body.Tools[0]
	require.Equal(t, "fs.write", plain.Name)
	require.Nil(t, plain.Annotations)
	require.Nil(t, plain.OutputSchema)
	require.Nil(t, plain.Meta)
}

func TestHandleTools_IncludesBackendStatuses(t *testing.T) {
	tools := &fakeToolAndBackendLister{
		tools: []server.Tool{{Name: "github.search"}},
		backends: []server.BackendStatus{
			{Name: "zeta", Status: "failed", Phase: "connect", Attempts: 2, Error: "connection refused"},
			{Name: "github", Status: "connected", Attempts: 1, ToolCount: 1},
		},
	}
	d := New(tools, nil, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/tools")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Tools    []server.Tool          `json:"tools"`
		Backends []server.BackendStatus `json:"backends"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, []server.Tool{{Name: "github.search"}}, body.Tools)
	require.Equal(t, []server.BackendStatus{
		{Name: "github", Status: "connected", Attempts: 1, ToolCount: 1},
		{Name: "zeta", Status: "failed", Phase: "connect", Attempts: 2, Error: "connection refused"},
	}, body.Backends)
}

func TestDashboardIndex_RenderToolsHTMLHandlesBackendStatuses(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to execute dashboard rendering JavaScript")
	}

	script := `
const fs = require('fs');
const html = fs.readFileSync('internal/dashboard/index.html', 'utf8');
const start = html.indexOf('      function renderToolsHTML(');
const end = html.indexOf('      function loadTools()', start);
if (start < 0 || end < 0) throw new Error('renderToolsHTML not found');
function esc(s) {
  return String(s || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
function escAttr(s) {
  return esc(s).replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
function renderToolHints() { return ''; }
eval(html.slice(start, end));
const rendered = renderToolsHTML(
  [{name: 'gh.enterprise.search', description: 'Search'}, {name: 'loose.tool'}],
  [
    {name: 'zeta', status: 'failed', phase: 'connect', attempts: 2, error: 'connection refused'},
    {name: 'gh.enterprise', status: 'connected', attempts: 1, tool_count: 1},
    {name: 'alpha', status: 'connected', attempts: 1, tool_count: 0}
  ]
);
function assert(condition, message) {
  if (!condition) throw new Error(message + '\n' + rendered);
}
const alpha = rendered.indexOf('> alpha<span class="provider-status connected">connected</span>');
const enterprise = rendered.indexOf('> gh.enterprise<span class="provider-status connected">connected</span>');
const loose = rendered.indexOf('> loose<span class="provider-status connected">connected</span>');
const zeta = rendered.indexOf('> zeta<span class="provider-status failed">failed</span>');
assert(alpha >= 0, 'connected zero-tool backend is rendered');
assert(enterprise > alpha, 'providers are sorted by backend name');
assert(loose > enterprise, 'tool-only provider falls back to first prefix');
assert(zeta > loose, 'failed backend is sorted with providers');
assert(rendered.includes('No tools discovered'), 'connected zero-tool backend shows empty state');
assert(rendered.includes('Failed during startup'), 'failed backend shows startup failure');
assert(rendered.includes('Phase: <code>connect</code>'), 'failed backend shows phase');
assert(rendered.includes('Error: connection refused'), 'failed backend shows error summary');
assert(rendered.includes('gh.enterprise.search'), 'dotted backend name keeps its tool');
assert(!rendered.includes('> gh<span'), 'dotted backend name is not split at first dot');
console.log('ok');
`

	cmd := exec.Command("node", "-e", script)
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "ok")
}

func TestDashboardIndex_IncludesBackendStatusRendering(t *testing.T) {
	d := New(nil, nil, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)
	require.Contains(t, html, "Failed during startup")
	require.Contains(t, html, "No tools discovered")
	require.Contains(t, html, "backendNames.find")
	require.Contains(t, html, "backendName + \".\"")
}

func TestHandleRules_GroupsToolsByMatchingRule(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{
		{Name: "github.list_prs"},
		{Name: "github.view_pr"},
		{Name: "github.delete_repo"},
		{Name: "fs.write"},
	}}
	rules := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "github.delete_*", Verdict: "deny"},
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "require-approval"},
	}}
	d := New(tools, rules, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Rules []struct {
			Index   int      `json:"index"`
			Tool    string   `json:"tool"`
			Verdict string   `json:"verdict"`
			Matches []string `json:"matches"`
		} `json:"rules"`
		AlwaysFallThrough []string `json:"always_fall_through"`
		MayFallThrough    []string `json:"may_fall_through"`
		DefaultVerdict    string   `json:"default_verdict"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Len(t, body.Rules, 3)

	require.Equal(t, 0, body.Rules[0].Index)
	require.Equal(t, "github.delete_*", body.Rules[0].Tool)
	require.Equal(t, "deny", body.Rules[0].Verdict)
	require.Equal(t, []string{"github.delete_repo"}, body.Rules[0].Matches)

	require.Equal(t, 1, body.Rules[1].Index)
	require.Equal(t, "github.*", body.Rules[1].Tool)
	require.Equal(t, "allow", body.Rules[1].Verdict)
	require.ElementsMatch(t, []string{"github.list_prs", "github.view_pr"}, body.Rules[1].Matches)

	require.Equal(t, 2, body.Rules[2].Index)
	require.Equal(t, "*", body.Rules[2].Tool)
	require.Equal(t, "require-approval", body.Rules[2].Verdict)
	require.Equal(t, []string{"fs.write"}, body.Rules[2].Matches)

	require.Empty(t, body.AlwaysFallThrough)
	require.Empty(t, body.MayFallThrough)
	require.Equal(t, "require-approval", body.DefaultVerdict)
}

func TestHandleRules_EmptyRules(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{{Name: "fs.write"}}}
	rules := &fakeRulesLister{rules: nil}
	d := New(tools, rules, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Rules             []any    `json:"rules"`
		AlwaysFallThrough []string `json:"always_fall_through"`
		MayFallThrough    []string `json:"may_fall_through"`
		DefaultVerdict    string   `json:"default_verdict"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.Rules)
	require.Equal(t, []string{"fs.write"}, body.AlwaysFallThrough)
	require.Empty(t, body.MayFallThrough)
	require.Equal(t, "require-approval", body.DefaultVerdict)
}

func TestHandleRules_ReflectsReloadedRulesStore(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{{Name: "github.search"}}}
	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "initial"}})
	require.NoError(t, err)
	d := New(tools, store, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	require.NoError(t, store.Reload([]config.RuleConfig{{Tool: "github.*", Verdict: "allow", Reason: "reloaded"}}))

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Rules []struct {
			Tool    string `json:"tool"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"rules"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Rules, 1)
	require.Equal(t, "github.*", body.Rules[0].Tool)
	require.Equal(t, "allow", body.Rules[0].Verdict)
	require.Equal(t, "reloaded", body.Rules[0].Reason)
}

func TestHandleRules_RuleWithNoMatches(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{{Name: "fs.write"}}}
	rules := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "require-approval"},
	}}
	d := New(tools, rules, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Rules []struct {
			Matches []string `json:"matches"`
		} `json:"rules"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Rules, 2)
	require.Empty(t, body.Rules[0].Matches) // github.* has no matches
	require.Equal(t, []string{"fs.write"}, body.Rules[1].Matches)
}

func TestHandleRules_NilLister(t *testing.T) {
	d := New(nil, nil, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Rules             []any    `json:"rules"`
		AlwaysFallThrough []string `json:"always_fall_through"`
		MayFallThrough    []string `json:"may_fall_through"`
		DefaultVerdict    string   `json:"default_verdict"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.Rules)
	require.Empty(t, body.AlwaysFallThrough)
	require.Empty(t, body.MayFallThrough)
	require.Equal(t, "require-approval", body.DefaultVerdict)
}

func TestHandleRules_MalformedGlobPattern(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{
		{Name: "github.list_prs"},
		{Name: "fs.write"},
	}}
	rules := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "[invalid", Verdict: "deny"},
		{Tool: "*", Verdict: "require-approval"},
	}}
	d := New(tools, rules, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Rules []struct {
			Index   int      `json:"index"`
			Tool    string   `json:"tool"`
			Matches []string `json:"matches"`
		} `json:"rules"`
		AlwaysFallThrough []string `json:"always_fall_through"`
		MayFallThrough    []string `json:"may_fall_through"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Len(t, body.Rules, 2)
	// Malformed rule is present but matches nothing (filepath.Match errors are silently skipped)
	require.Equal(t, "[invalid", body.Rules[0].Tool)
	require.Empty(t, body.Rules[0].Matches)
	// The catchall rule still catches both tools
	require.ElementsMatch(t, []string{"fs.write", "github.list_prs"}, body.Rules[1].Matches)
	require.Empty(t, body.AlwaysFallThrough)
	require.Empty(t, body.MayFallThrough)
}

func TestHandleRules_AgreesWithEngineEvaluateWithRule(t *testing.T) {
	ruleConfigs := []config.RuleConfig{
		{Tool: "github.delete_*", Verdict: "deny"},
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "fs.*", Verdict: "require-approval"},
		{Tool: "*", Verdict: "allow"},
	}
	engine, err := rules.New(ruleConfigs)
	require.NoError(t, err)

	toolList := []server.Tool{
		{Name: "github.list_prs"},
		{Name: "github.delete_repo"},
		{Name: "fs.write"},
		{Name: "linear.search"},
	}
	tools := &fakeToolLister{tools: toolList}

	d := New(tools, engine, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Rules []struct {
			Index   int      `json:"index"`
			Matches []string `json:"matches"`
		} `json:"rules"`
		AlwaysFallThrough []string `json:"always_fall_through"`
		MayFallThrough    []string `json:"may_fall_through"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	// Build expected mapping by asking the engine directly
	expectedMatches := make(map[int][]string, len(ruleConfigs))
	for i := range ruleConfigs {
		expectedMatches[i] = []string{}
	}
	var expectedAlways []string
	for _, tool := range toolList {
		_, idx := engine.EvaluateWithRule(tool.Name, nil)
		if idx >= 0 {
			expectedMatches[idx] = append(expectedMatches[idx], tool.Name)
		} else {
			expectedAlways = append(expectedAlways, tool.Name)
		}
	}

	// Compare handler output against the engine
	require.Len(t, body.Rules, len(ruleConfigs))
	for i, rv := range body.Rules {
		require.ElementsMatch(t, expectedMatches[i], rv.Matches, "rule %d (%s) mismatch", i, ruleConfigs[i].Tool)
	}
	if expectedAlways == nil {
		expectedAlways = []string{}
	}
	require.ElementsMatch(t, expectedAlways, body.AlwaysFallThrough)
	require.Empty(t, body.MayFallThrough)
}

func TestHandleRules_PassesThroughArgs(t *testing.T) {
	argPattern := config.ArgPattern{
		Path:  "remote",
		Match: json.RawMessage(`"origin"`),
	}
	tools := &fakeToolLister{tools: []server.Tool{{Name: "push"}}}
	rulesLister := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "push", Args: []config.ArgPattern{argPattern}, Verdict: "allow"},
	}}
	d := New(tools, rulesLister, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Rules []struct {
			Args []struct {
				Path  string          `json:"path"`
				Match json.RawMessage `json:"match"`
			} `json:"args"`
		} `json:"rules"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Rules, 1)
	require.Len(t, body.Rules[0].Args, 1)
	require.Equal(t, "remote", body.Rules[0].Args[0].Path)
	require.JSONEq(t, `"origin"`, string(body.Rules[0].Args[0].Match))
}

func TestHandleRules_MayFallThrough(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{
		{Name: "push"},
		{Name: "github.list_prs"},
	}}
	rulesLister := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "push", Args: []config.ArgPattern{{Path: "remote", Match: json.RawMessage(`"origin"`)}}, Verdict: "allow"},
		{Tool: "github.*", Verdict: "allow"},
	}}
	d := New(tools, rulesLister, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Rules []struct {
			Index   int      `json:"index"`
			Matches []string `json:"matches"`
		} `json:"rules"`
		AlwaysFallThrough []string `json:"always_fall_through"`
		MayFallThrough    []string `json:"may_fall_through"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	// push: its only name-matching rule is constrained → listed under that
	// rule (so users can see the rule targets it) AND in may_fall_through
	// (because args may not match, in which case it falls through to default).
	require.Equal(t, []string{"push"}, body.Rules[0].Matches)
	require.Equal(t, []string{"push"}, body.MayFallThrough)
	// github.list_prs: rule index 1 is unconstrained → Matches[1]
	require.Equal(t, 1, body.Rules[1].Index)
	require.Equal(t, []string{"github.list_prs"}, body.Rules[1].Matches)
	require.Empty(t, body.AlwaysFallThrough)
}

func TestHandleRules_AlwaysFallThrough(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{{Name: "linear.search"}}}
	rulesLister := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
	}}
	d := New(tools, rulesLister, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		AlwaysFallThrough []string `json:"always_fall_through"`
		MayFallThrough    []string `json:"may_fall_through"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, []string{"linear.search"}, body.AlwaysFallThrough)
	require.Empty(t, body.MayFallThrough)
}

func TestDashboard_OnAuditRecord_BroadcastsSSEFrame(t *testing.T) {
	d := New(nil, nil, nil, nil)

	// Register a fake SSE client by injecting a channel directly.
	ch := make(chan []byte, 4)
	d.mu.Lock()
	d.clients = append(d.clients, ch)
	d.mu.Unlock()

	approved := true
	rec := audit.Record{
		Timestamp:        time.Now().UTC().Truncate(time.Second),
		Tool:             "push",
		Args:             map[string]any{"remote": "origin"},
		Verdict:          "allow",
		Approved:         &approved,
		GrantID:          "grant-1",
		GrantName:        "release",
		GrantFingerprint: "abc123def456",
		GrantStatus:      "active",
		RuleSource:       "grant",
	}

	d.OnAuditRecord(rec)

	// Expect exactly one message on the channel.
	select {
	case frame := <-ch:
		var env struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(frame, &env))
		require.Equal(t, "audit", env.Type)

		var data struct {
			Tool             string `json:"tool"`
			Verdict          string `json:"verdict"`
			GrantID          string `json:"grant_id"`
			GrantFingerprint string `json:"grant_fingerprint"`
			GrantStatus      string `json:"grant_status"`
			RuleSource       string `json:"rule_source"`
		}
		require.NoError(t, json.Unmarshal(env.Data, &data))
		require.Equal(t, rec.Tool, data.Tool)
		require.Equal(t, rec.Verdict, data.Verdict)
		require.Equal(t, rec.GrantID, data.GrantID)
		require.Equal(t, rec.GrantFingerprint, data.GrantFingerprint)
		require.Equal(t, rec.GrantStatus, data.GrantStatus)
		require.Equal(t, rec.RuleSource, data.RuleSource)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no SSE frame received after OnAuditRecord")
	}
}

func TestHandleRules_ConstrainedThenUnconstrained(t *testing.T) {
	tools := &fakeToolLister{tools: []server.Tool{{Name: "push"}}}
	rulesLister := &fakeRulesLister{rules: []config.RuleConfig{
		{Tool: "push", Args: []config.ArgPattern{{Path: "remote", Match: json.RawMessage(`"origin"`)}}, Verdict: "allow"},
		{Tool: "p*", Verdict: "deny"},
	}}
	d := New(tools, rulesLister, nil, nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Rules []struct {
			Index   int      `json:"index"`
			Matches []string `json:"matches"`
		} `json:"rules"`
		AlwaysFallThrough []string `json:"always_fall_through"`
		MayFallThrough    []string `json:"may_fall_through"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	// push matches rule 0 (constrained) and rule 1 (unconstrained).
	// Both rules list push so users see every rule that may apply. The
	// unconstrained rule 1 guarantees a match, so push is NOT in
	// may_fall_through.
	require.Len(t, body.Rules, 2)
	require.Equal(t, []string{"push"}, body.Rules[0].Matches)
	require.Equal(t, []string{"push"}, body.Rules[1].Matches)
	require.Empty(t, body.MayFallThrough)
	require.Empty(t, body.AlwaysFallThrough)
}
