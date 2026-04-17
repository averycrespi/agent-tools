// Package rules loads and validates HCL rule files for agent-gateway.
package rules

import (
	"net/http"
	"regexp"
)

// Request is a normalised view of an HTTP request used for rule evaluation.
type Request struct {
	// Agent is the authenticated agent name.
	Agent string
	// Host is the target hostname (no port).
	Host string
	// Method is the HTTP method, expected in uppercase (e.g. "GET").
	Method string
	// Path is the request path (e.g. "/repos/octocat/Hello-World/issues").
	Path string
	// Header contains the canonical request headers.
	Header http.Header
}

// MatchResult is returned by Engine.Evaluate when a rule matches.
type MatchResult struct {
	// Rule is a pointer to the matching rule.
	Rule *Rule
	// Index is the zero-based position of the rule in the rule set.
	Index int
}

// Rule is the parsed, validated representation of a single rule block.
type Rule struct {
	Name    string
	Agents  []string // nil = all agents; empty slice = load-time error
	Match   Match
	Verdict string
	Inject  *Inject

	// Compiled matchers populated at load time (unexported).
	hostGlob  globMatcher
	pathGlob  globMatcher
	headerREs map[string]*regexp.Regexp
	body      bodyMatcher // nil when rule has no body block
}

// Match holds all match criteria for a rule.
type Match struct {
	Host    string
	Method  string
	Path    string
	Headers map[string]string

	// At most one body matcher per rule.
	JSONBody *JSONBodyMatch
	FormBody *FormBodyMatch
	TextBody *TextBodyMatch
}

// JSONBodyMatch holds json_body matchers.
type JSONBodyMatch struct {
	Paths []JSONPathMatcher
}

// JSONPathMatcher pairs a JSONPath expression with a regex.
type JSONPathMatcher struct {
	Path    string
	Matches string
	re      *regexp.Regexp
}

// FormBodyMatch holds form_body field matchers.
type FormBodyMatch struct {
	Fields []FormFieldMatcher
}

// FormFieldMatcher pairs a form field name with a regex.
type FormFieldMatcher struct {
	Field   string
	Matches string
	re      *regexp.Regexp
}

// TextBodyMatch holds a raw text body regex.
type TextBodyMatch struct {
	Matches string
	re      *regexp.Regexp
}

// Inject specifies headers to set or remove on matched requests.
type Inject struct {
	SetHeaders    map[string]string
	RemoveHeaders []string
}

// bodyMatcher is the sealed interface for compiled body matchers.
// It exists to make nil-check semantics clear at rule evaluation time.
type bodyMatcher interface {
	isBodyMatcher()
}

func (*JSONBodyMatch) isBodyMatcher() {}
func (*FormBodyMatch) isBodyMatcher() {}
func (*TextBodyMatch) isBodyMatcher() {}

// globMatcher is a compiled host/path glob.
type globMatcher struct {
	pattern string
	// re is the compiled regular expression derived from the glob pattern.
	// It is nil when the pattern is empty (no constraint).
	re *regexp.Regexp
}
