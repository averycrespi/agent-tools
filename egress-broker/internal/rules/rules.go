// Package rules defines the policy rule schema, its load-time validation, and
// the matching engine that decides what happens to a connection.
//
// The package split mirrors mcp-broker: rule *loading orchestration* lives in
// internal/config, while the schema and matching live here.
package rules

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/egress-broker/internal/glob"
	"github.com/averycrespi/agent-tools/egress-broker/internal/hostmatch"
	"github.com/averycrespi/agent-tools/egress-broker/internal/hostnorm"
)

// Mode is what a rule does to a connection it matches.
type Mode string

const (
	// ModeIntercept terminates TLS, evaluates the request, and may inject
	// credentials before forwarding it.
	ModeIntercept Mode = "intercept"
	// ModeTunnel relays bytes without terminating TLS. No injection is
	// possible, and no method or path is ever visible.
	ModeTunnel Mode = "tunnel"
	// ModeDeny rejects the connection or request.
	ModeDeny Mode = "deny"
)

// Fallthrough is the policy applied to a host no rule matches.
type Fallthrough string

const (
	// FallthroughTunnel relays unmatched hosts, auditing them.
	FallthroughTunnel Fallthrough = "tunnel"
	// FallthroughDeny rejects unmatched hosts.
	FallthroughDeny Fallthrough = "deny"
)

// DefaultTunnelPort is the only port a tunnel or deny decision applies to
// unless a rule opts others in via "ports". Tunnelling an arbitrary port
// turns the proxy into a generic TCP relay (AC-16).
const DefaultTunnelPort = 443

// Inject describes the header mutations an intercept rule applies.
//
// Set values may contain ${cred.<name>} references; every one is resolved and
// host-checked before any header is written (D17).
type Inject struct {
	Set    map[string]string `json:"set,omitempty"`
	Remove []string          `json:"remove,omitempty"`
}

// Rule is one policy entry as written in rules.json.
type Rule struct {
	Name   string  `json:"name"`
	Host   string  `json:"host"`
	Path   string  `json:"path,omitempty"`
	Method string  `json:"method,omitempty"`
	Ports  []int   `json:"ports,omitempty"`
	Mode   Mode    `json:"mode"`
	Inject *Inject `json:"inject,omitempty"`
}

// Document is the top-level rules.json shape.
type Document struct {
	Fallthrough Fallthrough `json:"fallthrough"`
	Rules       []Rule      `json:"rules"`
}

// HostOnly reports whether the rule is scoped by host alone — no path and no
// method. Only host-only rules can be applied at CONNECT time, because no
// request exists yet.
func (r Rule) HostOnly() bool { return r.Path == "" && r.Method == "" }

// compiled is one validated rule with its globs already compiled.
type compiled struct {
	rule Rule
	host *hostnorm.Glob
	path *glob.Glob
	// ports is the effective port set. Empty means "any port", which is the
	// default for intercept rules only.
	ports []int
}

// Engine is an immutable, compiled snapshot of a rules document.
type Engine struct {
	fallthrough_ Fallthrough
	rules        []*compiled
	doc          Document
}

// Fallthrough returns the snapshot's unmatched-host policy.
func (e *Engine) Fallthrough() Fallthrough { return e.fallthrough_ }

// Document returns a copy of the document the snapshot was compiled from.
func (e *Engine) Document() Document {
	out := Document{Fallthrough: e.doc.Fallthrough, Rules: slices.Clone(e.doc.Rules)}
	return out
}

// LoadError names the rule or file that failed validation.
type LoadError struct {
	Rule   string
	Detail string
}

func (e *LoadError) Error() string {
	if e.Rule == "" {
		return "rules: " + e.Detail
	}
	return fmt.Sprintf("rules: rule %q: %s", e.Rule, e.Detail)
}

// New validates and compiles a rules document into an immutable Engine.
//
// Every AC-6 condition is checked here so an invalid document can never reach
// the request path: a missing host, a tunnel rule carrying path or method,
// duplicate names, an unknown mode, a bad glob, two host-only rules of
// differing mode matching the same host, and any host glob that reduces to an
// ICANN public suffix.
func New(doc Document) (*Engine, error) {
	switch doc.Fallthrough {
	case FallthroughTunnel, FallthroughDeny:
	case "":
		return nil, &LoadError{Detail: `"fallthrough" is required and must be "tunnel" or "deny"`}
	default:
		return nil, &LoadError{Detail: fmt.Sprintf("unknown fallthrough %q: must be \"tunnel\" or \"deny\"", doc.Fallthrough)}
	}

	seen := make(map[string]struct{}, len(doc.Rules))
	out := make([]*compiled, 0, len(doc.Rules))

	for i, r := range doc.Rules {
		label := r.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}

		if r.Name == "" {
			return nil, &LoadError{Rule: label, Detail: `"name" is required; audit rows and the dashboard attribute by name`}
		}
		if _, dup := seen[r.Name]; dup {
			return nil, &LoadError{Rule: r.Name, Detail: "duplicate rule name"}
		}
		seen[r.Name] = struct{}{}

		c, err := compileRule(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}

	if err := checkHostOnlyConflicts(out); err != nil {
		return nil, err
	}

	return &Engine{fallthrough_: doc.Fallthrough, rules: out, doc: doc}, nil
}

func compileRule(r Rule) (*compiled, error) {
	if strings.TrimSpace(r.Host) == "" {
		return nil, &LoadError{Rule: r.Name, Detail: `"host" is required`}
	}

	switch r.Mode {
	case ModeIntercept, ModeTunnel, ModeDeny:
	case "":
		return nil, &LoadError{Rule: r.Name, Detail: `"mode" is required; must be "intercept", "tunnel", or "deny"`}
	default:
		return nil, &LoadError{Rule: r.Name, Detail: fmt.Sprintf("unknown mode %q: must be \"intercept\", \"tunnel\", or \"deny\"", r.Mode)}
	}

	// D5: tunnelling is decided at CONNECT, before any request exists, so a
	// tunnel rule cannot be scoped by anything only a request would reveal.
	if r.Mode == ModeTunnel && (r.Path != "" || r.Method != "") {
		return nil, &LoadError{
			Rule:   r.Name,
			Detail: `a "tunnel" rule must be host-only: the tunnel decision happens at CONNECT, before any path or method exists`,
		}
	}

	if r.Mode != ModeIntercept && r.Inject != nil {
		return nil, &LoadError{Rule: r.Name, Detail: fmt.Sprintf(`"inject" requires mode "intercept", not %q`, r.Mode)}
	}

	// Normalise before the suffix check, or "*.COM" and "*.com." would each
	// slip past a comparison against the Public Suffix List.
	normalizedHost, err := hostnorm.NormalizeGlob(r.Host)
	if err != nil {
		return nil, &LoadError{Rule: r.Name, Detail: fmt.Sprintf("invalid host glob %q: %v", r.Host, err)}
	}
	if normalizedHost == "*" || normalizedHost == "**" {
		return nil, &LoadError{Rule: r.Name, Detail: fmt.Sprintf("host %q matches every host; name the hosts this rule is for", r.Host)}
	}
	if isSuffix, suffix := hostmatch.MatchesPublicSuffix(normalizedHost); isSuffix {
		return nil, &LoadError{
			Rule:   r.Name,
			Detail: fmt.Sprintf("host %q reduces to the public suffix %q, matching every host under a registry-controlled TLD", r.Host, suffix),
		}
	}

	hostGlob, err := hostnorm.Compile(r.Host)
	if err != nil {
		return nil, &LoadError{Rule: r.Name, Detail: fmt.Sprintf("invalid host glob %q: %v", r.Host, err)}
	}

	var pathGlob *glob.Glob
	if r.Path != "" {
		pathGlob, err = compilePathGlob(r.Path)
		if err != nil {
			return nil, &LoadError{Rule: r.Name, Detail: fmt.Sprintf("invalid path glob %q: %v", r.Path, err)}
		}
	}

	if r.Method != "" && !isValidMethod(r.Method) {
		return nil, &LoadError{Rule: r.Name, Detail: fmt.Sprintf("invalid method %q", r.Method)}
	}

	for _, p := range r.Ports {
		if p < 1 || p > 65535 {
			return nil, &LoadError{Rule: r.Name, Detail: fmt.Sprintf("port %d out of range 1-65535", p)}
		}
	}

	return &compiled{
		rule:  normalizeRule(r),
		host:  hostGlob,
		path:  pathGlob,
		ports: effectivePorts(r),
	}, nil
}

// effectivePorts resolves the ports a rule applies to.
//
// Tunnel and deny default to 443 only: a tunnel decision is a decision to
// relay opaque bytes, and defaulting that to every port would make the proxy a
// generic TCP relay. Intercept defaults to any port, because an intercepted
// connection is still parsed and evaluated per request (AC-16).
func effectivePorts(r Rule) []int {
	if len(r.Ports) > 0 {
		ports := slices.Clone(r.Ports)
		slices.Sort(ports)
		return slices.Compact(ports)
	}
	if r.Mode == ModeIntercept {
		return nil
	}
	return []int{DefaultTunnelPort}
}

func normalizeRule(r Rule) Rule {
	out := r
	out.Method = strings.ToUpper(r.Method)
	return out
}

// checkHostOnlyConflicts rejects two host-only rules of differing mode whose
// globs are written identically. Step 3 of the CONNECT procedure applies "the"
// host-only rule for a host; if two disagreed, the outcome would depend on
// rule order, which is exactly the ambiguity AC-6 forbids.
func checkHostOnlyConflicts(rules []*compiled) error {
	byHost := make(map[string]*compiled)
	for _, c := range rules {
		if !c.rule.HostOnly() {
			continue
		}
		key := c.host.String()
		prev, ok := byHost[key]
		if !ok {
			byHost[key] = c
			continue
		}
		if prev.rule.Mode != c.rule.Mode {
			return &LoadError{
				Rule: c.rule.Name,
				Detail: fmt.Sprintf("host-only rule for %q has mode %q but rule %q already covers the same host with mode %q; a CONNECT for that host would have no unambiguous outcome",
					c.rule.Host, c.rule.Mode, prev.rule.Name, prev.rule.Mode),
			}
		}
	}
	return nil
}

// compilePathGlob compiles a URL-path glob. Paths use "/" as the separator,
// with the same "*" (within a segment) and "**" (across segments) semantics
// host globs use — the same translation, from the same package (D16).
func compilePathGlob(pattern string) (*glob.Glob, error) {
	if !strings.HasPrefix(pattern, "/") {
		return nil, errors.New(`path glob must start with "/"`)
	}
	return glob.Compile(pattern, '/')
}

// isValidMethod reports whether s is an HTTP method token we recognise.
func isValidMethod(s string) bool {
	switch strings.ToUpper(s) {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// RuleNames returns the snapshot's rule names in evaluation order. The
// dashboard uses this; it carries no credential material.
func (e *Engine) RuleNames() []string {
	out := make([]string, 0, len(e.rules))
	for _, c := range e.rules {
		out = append(out, c.rule.Name)
	}
	sort.Strings(out)
	return out
}
