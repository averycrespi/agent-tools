package rules_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRequest constructs a Request with body fields set.
func buildRequest(method, host, path, ct string, body []byte) *rules.Request {
	h := make(http.Header)
	if ct != "" {
		h.Set("Content-Type", ct)
	}
	return &rules.Request{
		Agent:  "x",
		Host:   host,
		Method: method,
		Path:   path,
		Header: h,
		Body:   body,
	}
}

// TestBody_JSONPathMatch verifies that a json_body rule matches when the
// JSONPath value matches the regex and the Content-Type is application/json.
func TestBody_JSONPathMatch(t *testing.T) {
	rs := parseInline(t, `
rule "json-match" {
  match {
    host = "api.example.com"
    path = "/**"
    json_body {
      jsonpath "$.title" { matches = "^my-" }
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Matching body.
	req := buildRequest("POST", "api.example.com", "/issues", "application/json", []byte(`{"title":"my-issue"}`))
	m := engine.Evaluate(req)
	require.NotNil(t, m)
	assert.Equal(t, "json-match", m.Rule.Name)

	// Body with non-matching title.
	req2 := buildRequest("POST", "api.example.com", "/issues", "application/json", []byte(`{"title":"other"}`))
	m2 := engine.Evaluate(req2)
	assert.Nil(t, m2)

	// Content-Type with charset should still match.
	req3 := buildRequest("POST", "api.example.com", "/issues", "application/json; charset=utf-8", []byte(`{"title":"my-issue"}`))
	m3 := engine.Evaluate(req3)
	require.NotNil(t, m3)
	assert.Equal(t, "json-match", m3.Rule.Name)
}

// TestBody_JSONPathWildcard verifies that $.labels[*] iterates all elements
// and matches when any element matches the regex.
func TestBody_JSONPathWildcard(t *testing.T) {
	rs := parseInline(t, `
rule "has-bug-label" {
  match {
    host = "api.example.com"
    path = "/**"
    json_body {
      jsonpath "$.labels[*]" { matches = "^bug$" }
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	req := buildRequest("POST", "api.example.com", "/issues", "application/json",
		[]byte(`{"labels":["enhancement","bug"]}`))
	m := engine.Evaluate(req)
	require.NotNil(t, m)
	assert.Equal(t, "has-bug-label", m.Rule.Name)

	// None of the labels match.
	req2 := buildRequest("POST", "api.example.com", "/issues", "application/json",
		[]byte(`{"labels":["feature","docs"]}`))
	m2 := engine.Evaluate(req2)
	assert.Nil(t, m2)
}

// TestBody_JSONBlockRejectsNonJSONContentType verifies that a json_body rule
// does not match when the Content-Type is not application/json.
func TestBody_JSONBlockRejectsNonJSONContentType(t *testing.T) {
	rs := parseInline(t, `
rule "json-match" {
  match {
    host = "api.example.com"
    path = "/**"
    json_body {
      jsonpath "$.title" { matches = ".*" }
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Content-Type is text/plain — must not match even if body is valid JSON.
	req := buildRequest("POST", "api.example.com", "/issues", "text/plain", []byte(`{"title":"anything"}`))
	m := engine.Evaluate(req)
	assert.Nil(t, m)

	// No Content-Type at all.
	req2 := buildRequest("POST", "api.example.com", "/issues", "", []byte(`{"title":"anything"}`))
	m2 := engine.Evaluate(req2)
	assert.Nil(t, m2)

	// application/x-www-form-urlencoded — must not match.
	req3 := buildRequest("POST", "api.example.com", "/issues", "application/x-www-form-urlencoded", []byte(`{"title":"anything"}`))
	m3 := engine.Evaluate(req3)
	assert.Nil(t, m3)
}

// TestBody_EmptyBodyNeverMatches verifies that requests without a body (e.g.
// GET with no body, or POST with an empty body) never match body-matcher rules.
func TestBody_EmptyBodyNeverMatches(t *testing.T) {
	rs := parseInline(t, `
rule "json-match" {
  match {
    host = "api.example.com"
    path = "/**"
    json_body {
      jsonpath "$.x" { matches = ".*" }
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// GET with no body — Body is nil.
	req := buildRequest("GET", "api.example.com", "/issues", "application/json", nil)
	m := engine.Evaluate(req)
	assert.Nil(t, m, "GET with nil body must not match body-matcher rule")

	// POST with explicitly empty body (Content-Length: 0 equivalent).
	req2 := buildRequest("POST", "api.example.com", "/issues", "application/json", []byte{})
	m2 := engine.Evaluate(req2)
	assert.Nil(t, m2, "POST with empty body must not match body-matcher rule")
}

// TestBody_FormBodyFieldRegex verifies that a form_body rule matches when
// the form field matches the regex and Content-Type is application/x-www-form-urlencoded.
func TestBody_FormBodyFieldRegex(t *testing.T) {
	rs := parseInline(t, `
rule "client-credentials" {
  match {
    host = "auth.example.com"
    path = "/token"
    form_body {
      field "grant_type" { matches = "^client_credentials$" }
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Matching form body.
	body := []byte("grant_type=client_credentials&client_id=abc")
	req := buildRequest("POST", "auth.example.com", "/token", "application/x-www-form-urlencoded", body)
	m := engine.Evaluate(req)
	require.NotNil(t, m)
	assert.Equal(t, "client-credentials", m.Rule.Name)

	// Wrong grant_type.
	body2 := []byte("grant_type=authorization_code&code=xyz")
	req2 := buildRequest("POST", "auth.example.com", "/token", "application/x-www-form-urlencoded", body2)
	m2 := engine.Evaluate(req2)
	assert.Nil(t, m2)

	// Wrong Content-Type (application/json) — must not match.
	req3 := buildRequest("POST", "auth.example.com", "/token", "application/json", body)
	m3 := engine.Evaluate(req3)
	assert.Nil(t, m3)
}

// TestBody_TextBodyRegex verifies that a text_body rule matches when the raw
// body matches the regex and Content-Type starts with "text/".
func TestBody_TextBodyRegex(t *testing.T) {
	rs := parseInline(t, `
rule "deploy-token" {
  match {
    host = "hooks.example.com"
    path = "/webhook"
    text_body {
      matches = "deploy-token-v2"
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Matching text body.
	body := []byte("action=deploy&token=deploy-token-v2&env=prod")
	req := buildRequest("POST", "hooks.example.com", "/webhook", "text/plain", body)
	m := engine.Evaluate(req)
	require.NotNil(t, m)
	assert.Equal(t, "deploy-token", m.Rule.Name)

	// text/html should also be accepted (text/*).
	req2 := buildRequest("POST", "hooks.example.com", "/webhook", "text/html", body)
	m2 := engine.Evaluate(req2)
	require.NotNil(t, m2)
	assert.Equal(t, "deploy-token", m2.Rule.Name)

	// Body does not contain the token.
	req3 := buildRequest("POST", "hooks.example.com", "/webhook", "text/plain", []byte("action=deploy"))
	m3 := engine.Evaluate(req3)
	assert.Nil(t, m3)

	// application/json Content-Type — must not match.
	req4 := buildRequest("POST", "hooks.example.com", "/webhook", "application/json", body)
	m4 := engine.Evaluate(req4)
	assert.Nil(t, m4)
}

// TestBody_OverSizeCapBypasses verifies that when BodyTruncated is true, body
// matchers auto-fail and the MatchResult carries the bypass reason.
func TestBody_OverSizeCapBypasses(t *testing.T) {
	rs := parseInline(t, `
rule "json-match" {
  match {
    host = "api.example.com"
    path = "/**"
    json_body {
      jsonpath "$.title" { matches = ".*" }
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// A request that would normally match, but body is truncated.
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	req := &rules.Request{
		Agent:         "x",
		Host:          "api.example.com",
		Method:        "POST",
		Path:          "/issues",
		Header:        h,
		Body:          []byte(strings.Repeat("x", 1024*1024+1)),
		BodyTruncated: true,
	}

	m := engine.Evaluate(req)
	// The rule must NOT match (body too large), but a MatchResult is returned
	// with the bypass error so the audit logger can record it.
	require.NotNil(t, m, "oversized body should return a MatchResult with bypass error")
	assert.Equal(t, "body_matcher_bypassed:size", m.Error)
	assert.Equal(t, "json-match", m.Rule.Name)
}

// TestBody_TimeoutBypasses verifies that when BodyTimedOut is true, body
// matchers auto-fail with the timeout bypass reason.
func TestBody_TimeoutBypasses(t *testing.T) {
	rs := parseInline(t, `
rule "json-match" {
  match {
    host = "api.example.com"
    path = "/**"
    json_body {
      jsonpath "$.title" { matches = ".*" }
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	req := &rules.Request{
		Agent:        "x",
		Host:         "api.example.com",
		Method:       "POST",
		Path:         "/issues",
		Header:       h,
		Body:         []byte(`{"title":"hello"}`),
		BodyTimedOut: true,
	}

	m := engine.Evaluate(req)
	require.NotNil(t, m)
	assert.Equal(t, "body_matcher_bypassed:timeout", m.Error)
	assert.Equal(t, "json-match", m.Rule.Name)
}

// TestBody_NoBodyMatcherRuleStillMatches verifies that rules without body
// matchers are unaffected by body-related fields on the Request.
func TestBody_NoBodyMatcherRuleStillMatches(t *testing.T) {
	rs := parseInline(t, `
rule "no-body" {
  match {
    host = "api.example.com"
    path = "/**"
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	h := make(http.Header)
	req := &rules.Request{
		Agent:         "x",
		Host:          "api.example.com",
		Method:        "GET",
		Path:          "/foo",
		Header:        h,
		BodyTruncated: true, // even with bypass flags set, non-body rules should match
	}
	m := engine.Evaluate(req)
	require.NotNil(t, m)
	assert.Equal(t, "no-body", m.Rule.Name)
	assert.Empty(t, m.Error)
}
