package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/inject"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingLogger wraps audit.Logger and captures the most recently recorded entry.
type capturingLogger struct {
	logger audit.Logger
	last   *audit.Entry
}

func (c *capturingLogger) Record(ctx context.Context, e audit.Entry) error {
	c.last = &e
	return c.logger.Record(ctx, e)
}

func (c *capturingLogger) Query(ctx context.Context, f audit.Filter) ([]audit.Entry, error) {
	return c.logger.Query(ctx, f)
}

func (c *capturingLogger) Count(ctx context.Context, f audit.Filter) (int, error) {
	return c.logger.Count(ctx, f)
}

func (c *capturingLogger) Prune(ctx context.Context, before time.Time) (int, error) {
	return c.logger.Prune(ctx, before)
}

// newCapturingLogger returns a capturingLogger backed by a real SQLite db.
func newCapturingLogger(t *testing.T) *capturingLogger {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &capturingLogger{logger: audit.NewLogger(db)}
}

// okRoundTripper returns a simple 200 OK upstream stub.
func okRoundTripper() roundTripperFunc {
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})
}

// allowRuleWithInjectAndName returns a MatchResult for an allow rule with the
// given name and a inject block that references a secret.
func allowRuleWithInjectAndName(name string) *rules.MatchResult {
	return &rules.MatchResult{
		Rule: &rules.Rule{
			Name:    name,
			Verdict: "allow",
			Inject: &rules.Inject{
				ReplaceHeaders: map[string]string{
					"Authorization": "Bearer ${secrets.gh_bot}",
				},
			},
		},
	}
}

// denyRuleWithName returns a MatchResult for a deny rule with the given name.
func denyRuleWithName(name string) *rules.MatchResult {
	return &rules.MatchResult{
		Rule: &rules.Rule{Name: name, Verdict: "deny"},
	}
}

// requireApprovalRuleWithName returns a MatchResult for a require-approval rule
// with an inject block that references a secret, matching the §5 design rows
// where an approved require-approval request proceeds to credential injection.
func requireApprovalRuleWithName(name string) *rules.MatchResult {
	return &rules.MatchResult{
		Rule: &rules.Rule{
			Name:    name,
			Verdict: "require-approval",
			Inject: &rules.Inject{
				ReplaceHeaders: map[string]string{
					"Authorization": "Bearer ${secrets.deploy_key}",
				},
			},
		},
	}
}

// sendAuditRequest fires a request through the proxy and waits for the audit
// logger to capture the entry (up to ~100 ms). Returns the captured entry.
func sendAuditRequest(t *testing.T, p *proxy.Proxy, cl *capturingLogger, method, host, path string) *audit.Entry {
	t.Helper()
	r := httptest.NewRequest(method, "https://"+host+path, nil)
	w := httptest.NewRecorder()
	p.HandleForTest(w, r, host, "")
	// The defer in handle runs synchronously, so cl.last is populated when HandleForTest returns.
	return cl.last
}

// TestPipeline_Audit_MitmNoRule covers §5 row 2: MITM'd host, no rule matched,
// request forwarded with matched_rule=nil, injection=nil, outcome=forwarded.
func TestPipeline_Audit_MitmNoRule(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(nil), // no rule match
		Auditor:              cl,
	})

	e := sendAuditRequest(t, p, cl, http.MethodGet, "api.example.com:443", "/unmatched")
	require.NotNil(t, e)
	assert.Equal(t, "mitm", e.Interception)
	assert.Nil(t, e.MatchedRule)
	assert.Nil(t, e.Injection)
	assert.Equal(t, "forwarded", e.Outcome)
}

// TestPipeline_Audit_HappyPathAllow covers §5 row 3: allow rule matched,
// injection='applied', outcome=forwarded.
func TestPipeline_Audit_HappyPathAllow(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	inj := &stubInjector{
		headerToSet: map[string]string{"Authorization": "Bearer real-token"},
		scope:       "global",
	}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(allowRuleWithInjectAndName("github-issues")),
		Injector:             inj,
		Auditor:              cl,
	})

	e := sendAuditRequest(t, p, cl, http.MethodGet, "api.github.com:443", "/repos")
	require.NotNil(t, e)
	assert.Equal(t, "mitm", e.Interception)
	assert.Equal(t, "github-issues", *e.MatchedRule)
	assert.Equal(t, "allow", *e.RuleVerdict)
	assert.Equal(t, "applied", *e.Injection)
	assert.Equal(t, "gh_bot", *e.CredentialRef)
	assert.Equal(t, "global", *e.CredentialScope)
	assert.Equal(t, "forwarded", e.Outcome)
}

// TestPipeline_Audit_SecretUnresolved covers the fail-closed secret-unresolved
// path: allow rule matched but secret unresolved, injection='failed',
// error='secret_unresolved', outcome='blocked'.
func TestPipeline_Audit_SecretUnresolved(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	inj := &stubInjector{
		err: inject.ErrSecretUnresolved,
	}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(allowRuleWithInjectAndName("github-issues")),
		Injector:             inj,
		Auditor:              cl,
	})

	e := sendAuditRequest(t, p, cl, http.MethodGet, "api.github.com:443", "/issues")
	require.NotNil(t, e)
	assert.Equal(t, "mitm", e.Interception)
	assert.Equal(t, "github-issues", *e.MatchedRule)
	assert.Equal(t, "failed", *e.Injection)
	assert.Equal(t, "secret_unresolved", *e.Error)
	assert.Equal(t, "blocked", e.Outcome)
}

// TestPipeline_Audit_Deny covers §5 row 8: deny rule, outcome='blocked'.
func TestPipeline_Audit_Deny(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(denyRuleWithName("block-all-delete")),
		Auditor:              cl,
	})

	e := sendAuditRequest(t, p, cl, http.MethodDelete, "api.example.com:443", "/delete")
	require.NotNil(t, e)
	assert.Equal(t, "mitm", e.Interception)
	assert.Equal(t, "block-all-delete", *e.MatchedRule)
	assert.Equal(t, "deny", *e.RuleVerdict)
	assert.Nil(t, e.Injection)
	assert.Equal(t, "blocked", e.Outcome)
}

// TestPipeline_Audit_ApprovalApproved covers §5 row 5: require-approval,
// approved, injection='applied', forwarded.
func TestPipeline_Audit_ApprovalApproved(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	inj := &stubInjector{
		headerToSet: map[string]string{"Authorization": "Bearer deploy-token"},
		scope:       "agent:agent1",
	}

	broker := &stubApprovalBroker{decision: proxy.DecisionApproved}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(requireApprovalRuleWithName("prod-deploy")),
		Approval:             broker,
		Injector:             inj,
		Auditor:              cl,
	})

	e := sendAuditRequest(t, p, cl, http.MethodPost, "deploy.example.com:443", "/release")
	require.NotNil(t, e)
	assert.Equal(t, "mitm", e.Interception)
	assert.Equal(t, "prod-deploy", *e.MatchedRule)
	assert.Equal(t, "require-approval", *e.RuleVerdict)
	assert.Equal(t, "approved", *e.Approval)
	assert.Equal(t, "applied", *e.Injection)
	assert.Equal(t, "forwarded", e.Outcome)
}

// TestPipeline_Audit_ApprovalDenied covers §5 row 6: require-approval,
// denied, injection=nil, outcome='blocked'.
func TestPipeline_Audit_ApprovalDenied(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	broker := &stubApprovalBroker{decision: proxy.DecisionDenied}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(requireApprovalRuleWithName("prod-deploy")),
		Approval:             broker,
		Auditor:              cl,
	})

	e := sendAuditRequest(t, p, cl, http.MethodPost, "deploy.example.com:443", "/release")
	require.NotNil(t, e)
	assert.Equal(t, "mitm", e.Interception)
	assert.Equal(t, "prod-deploy", *e.MatchedRule)
	assert.Equal(t, "require-approval", *e.RuleVerdict)
	assert.Equal(t, "denied", *e.Approval)
	assert.Nil(t, e.Injection)
	assert.Equal(t, "blocked", e.Outcome)
}

// TestPipeline_Audit_ApprovalTimeout covers §5 row 7: require-approval,
// timed-out, outcome='blocked'.
func TestPipeline_Audit_ApprovalTimeout(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	broker := &stubApprovalBroker{decision: proxy.DecisionTimeout}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(requireApprovalRuleWithName("prod-deploy")),
		Approval:             broker,
		Auditor:              cl,
	})

	e := sendAuditRequest(t, p, cl, http.MethodPost, "deploy.example.com:443", "/release")
	require.NotNil(t, e)
	assert.Equal(t, "mitm", e.Interception)
	assert.Equal(t, "prod-deploy", *e.MatchedRule)
	assert.Equal(t, "require-approval", *e.RuleVerdict)
	assert.Equal(t, "timed-out", *e.Approval)
	assert.Nil(t, e.Injection)
	assert.Equal(t, "blocked", e.Outcome)
}

// TestPipeline_Audit_UnknownVerdict covers the unknown-verdict fail-closed path:
// the audit row must record error='unknown_verdict' and outcome='blocked'.
func TestPipeline_Audit_UnknownVerdict(t *testing.T) {
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(unknownVerdictMatchResult()),
		Auditor:              cl,
	})

	e := sendAuditRequest(t, p, cl, http.MethodGet, "example.com:443", "/hello")
	require.NotNil(t, e)
	assert.Equal(t, "mitm", e.Interception)
	assert.Equal(t, "future-rule", *e.MatchedRule)
	require.NotNil(t, e.Error)
	assert.Equal(t, "unknown_verdict", *e.Error)
	assert.Equal(t, "blocked", e.Outcome)
}

// runPipelineWithRawQuery builds a proxy with a memory auditor and an
// allow-all rules engine (nil match), fires a GET request to
// https://api.example.com:443/query with the given RawQuery, and returns the
// response recorder and the captured audit entries.
func runPipelineWithRawQuery(t *testing.T, raw string) (*httptest.ResponseRecorder, []audit.Entry) {
	t.Helper()
	auth := newTestAuthority(t)
	cl := newCapturingLogger(t)

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		Rules:                stubEngineReturning(nil), // nil match → allow
		Auditor:              cl,
	})

	r := httptest.NewRequest(http.MethodGet, "https://api.example.com:443/query", nil)
	r.URL.RawQuery = raw
	w := httptest.NewRecorder()
	p.HandleForTest(w, r, "api.example.com:443", "")

	var entries []audit.Entry
	if cl.last != nil {
		entries = append(entries, *cl.last)
	}
	return w, entries
}

// TestPipeline_StoresQueryStringWithTruncation verifies that a query string
// longer than 2048 bytes is truncated and suffixed with "…".
func TestPipeline_StoresQueryStringWithTruncation(t *testing.T) {
	long := strings.Repeat("a=1&", 1024) // 4096 bytes
	rec, entries := runPipelineWithRawQuery(t, long)
	_ = rec
	require.NotNil(t, entries[0].Query)
	require.True(t, strings.HasSuffix(*entries[0].Query, "…"))
	require.LessOrEqual(t, len(*entries[0].Query), 2048+len("…"))
}

// TestPipeline_StoresShortQueryVerbatim verifies that a short query string is
// stored unchanged.
func TestPipeline_StoresShortQueryVerbatim(t *testing.T) {
	_, entries := runPipelineWithRawQuery(t, "sort=updated&page=2")
	require.NotNil(t, entries[0].Query)
	require.Equal(t, "sort=updated&page=2", *entries[0].Query)
}

// TestPipeline_Audit_NilAuditor verifies that a nil Auditor does not panic.
func TestPipeline_Audit_NilAuditor(t *testing.T) {
	auth := newTestAuthority(t)

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: okRoundTripper(),
		// Auditor intentionally nil
	})

	r := httptest.NewRequest(http.MethodGet, "https://example.com:443/ok", nil)
	w := httptest.NewRecorder()
	require.NotPanics(t, func() {
		p.HandleForTest(w, r, "example.com:443", "")
	})
}
