package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
	"github.com/stretchr/testify/require"
)

type fakeToolLister struct{ tools []server.Tool }

func (f *fakeToolLister) Tools() []server.Tool { return f.tools }

type fakeRulesLister struct{ rules []config.RuleConfig }

func (f *fakeRulesLister) Rules() []config.RuleConfig { return f.rules }

func TestDashboard_Review_ApprovesViaAPI(t *testing.T) {
	d := New(nil, nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Start a review in a goroutine
	done := make(chan bool, 1)
	go func() {
		approved, _, err := d.Review(context.Background(), "github.push", map[string]any{"branch": "main"})
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
		approved, reason, err := d.Review(context.Background(), "github.push", map[string]any{})
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
		approved, reason, err := d.Review(ctx, "test.tool", nil)
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
		_, _, _ = d.Review(ctx, "test.tool", nil)
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
		Unmatched      []string `json:"unmatched"`
		DefaultVerdict string   `json:"default_verdict"`
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

	require.Empty(t, body.Unmatched)
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
		Rules          []any    `json:"rules"`
		Unmatched      []string `json:"unmatched"`
		DefaultVerdict string   `json:"default_verdict"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.Rules)
	require.Equal(t, []string{"fs.write"}, body.Unmatched)
	require.Equal(t, "require-approval", body.DefaultVerdict)
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
		Rules          []any    `json:"rules"`
		Unmatched      []string `json:"unmatched"`
		DefaultVerdict string   `json:"default_verdict"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.Rules)
	require.Empty(t, body.Unmatched)
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
		Unmatched []string `json:"unmatched"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Len(t, body.Rules, 2)
	// Malformed rule is present but matches nothing (filepath.Match errors are silently skipped)
	require.Equal(t, "[invalid", body.Rules[0].Tool)
	require.Empty(t, body.Rules[0].Matches)
	// The catchall rule still catches both tools
	require.ElementsMatch(t, []string{"fs.write", "github.list_prs"}, body.Rules[1].Matches)
	require.Empty(t, body.Unmatched)
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
		Unmatched []string `json:"unmatched"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	// Build expected mapping by asking the engine directly
	expectedMatches := make(map[int][]string, len(ruleConfigs))
	for i := range ruleConfigs {
		expectedMatches[i] = []string{}
	}
	var expectedUnmatched []string
	for _, tool := range toolList {
		_, idx := engine.EvaluateWithRule(tool.Name, nil)
		if idx >= 0 {
			expectedMatches[idx] = append(expectedMatches[idx], tool.Name)
		} else {
			expectedUnmatched = append(expectedUnmatched, tool.Name)
		}
	}

	// Compare handler output against the engine
	require.Len(t, body.Rules, len(ruleConfigs))
	for i, rv := range body.Rules {
		require.ElementsMatch(t, expectedMatches[i], rv.Matches, "rule %d (%s) mismatch", i, ruleConfigs[i].Tool)
	}
	if expectedUnmatched == nil {
		expectedUnmatched = []string{}
	}
	require.ElementsMatch(t, expectedUnmatched, body.Unmatched)
}
