// Package rules defines the policy rule schema, its load-time validation, and
// the matching engine that decides what happens to a connection.
//
// The package split mirrors mcp-broker: rule *loading orchestration* lives in
// internal/config, while the schema and matching live here.
package rules

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/http-broker/internal/glob"
	"github.com/averycrespi/agent-tools/http-broker/internal/hostmatch"
	"github.com/averycrespi/agent-tools/http-broker/internal/hostnorm"
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

// DefaultTunnelPort is the only port a tunnel decision applies to unless a
// rule opts others in via "ports". Tunnelling an arbitrary port turns the
// proxy into a generic TCP relay (AC-16).
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
	// AllowPrivate permits this rule's hosts to resolve into RFC 1918 or RFC
	// 4193 private space, which netguard otherwise refuses.
	//
	// It exists because an internal service is a legitimate target for
	// injection — often the main one — and the guard cannot tell a corporate
	// ALB from an SSRF attempt by address alone. Only a rule naming the host
	// can, so the grant is written where the host is.
	//
	// It relaxes exactly that one address class. Loopback, link-local,
	// multicast, unspecified, the reserved prefixes and the cloud-metadata
	// addresses stay refused, so this can never be an SSRF off switch.
	AllowPrivate bool `json:"allow_private,omitempty"`
	// MinTLSVersion lowers the upstream TLS floor for this rule's hosts, as
	// "1.2" or "1.3". Empty means the default, which is 1.3.
	//
	// The default is stricter than most of the internet: an AWS ALB on an older
	// security policy tops out at TLS 1.2 and closes the connection rather than
	// sending a version alert, so the failure arrives as a bare EOF. Rather
	// than lower the floor for every upstream, a rule naming the host can opt
	// that host down. Verification is unaffected — the certificate must still
	// chain to the system trust store either way.
	MinTLSVersion string `json:"min_tls_version,omitempty"`
}

// TLS floor values a rule may name, and the default when it names none.
const (
	// DefaultMinTLSVersion is the floor for any host no rule lowers (D12).
	DefaultMinTLSVersion = tls.VersionTLS13
)

// minTLSVersions is the accepted set. TLS 1.0 and 1.1 are deliberately absent:
// they are deprecated, and a rule field is not a reason to make them reachable.
var minTLSVersions = map[string]uint16{
	"1.2": tls.VersionTLS12,
	"1.3": tls.VersionTLS13,
}

// ResolvedMinTLSVersion returns the rule's floor, or the default when unset.
func (r Rule) ResolvedMinTLSVersion() uint16 {
	if v, ok := minTLSVersions[r.MinTLSVersion]; ok {
		return v
	}
	return DefaultMinTLSVersion
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
	// default for every mode except tunnel.
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

	if r.MinTLSVersion != "" {
		if _, ok := minTLSVersions[r.MinTLSVersion]; !ok {
			return nil, &LoadError{
				Rule:   r.Name,
				Detail: fmt.Sprintf(`unknown "min_tls_version" %q: must be "1.2" or "1.3"`, r.MinTLSVersion),
			}
		}
		// Only interception makes a TLS connection on the agent's behalf. A
		// tunnel relays the client's own handshake untouched, and a deny rule
		// connects to nothing, so on either the field would be inert.
		if r.Mode != ModeIntercept {
			return nil, &LoadError{
				Rule:   r.Name,
				Detail: fmt.Sprintf(`"min_tls_version" requires mode "intercept", not %q: only interception makes the upstream TLS connection`, r.Mode),
			}
		}
		// The floor is a property of the upstream connection, which is per host
		// and port, so it cannot be scoped by path or method — same reasoning as
		// allow_private.
		if !r.HostOnly() {
			return nil, &LoadError{
				Rule:   r.Name,
				Detail: `"min_tls_version" must be written on a host-only rule: the floor applies to the upstream connection, which is per host and port, so scoping it by path or method would claim to be narrower than it is`,
			}
		}
	}

	if r.AllowPrivate {
		// A deny rule dials nothing, so the grant would be inert; saying so is
		// better than accepting a line that reads like it does something.
		if r.Mode == ModeDeny {
			return nil, &LoadError{
				Rule:   r.Name,
				Detail: `"allow_private" requires mode "intercept" or "tunnel": a "deny" rule never dials, so the grant would do nothing`,
			}
		}
		// The grant takes effect on a dial, which is per host and port. Written
		// on a path- or method-scoped rule it would read as covering only that
		// path while actually covering every request to the host, so the
		// misleading form is rejected rather than silently widened.
		if !r.HostOnly() {
			return nil, &LoadError{
				Rule:   r.Name,
				Detail: `"allow_private" must be written on a host-only rule: the grant applies to the upstream dial, which is per host and port, so scoping it by path or method would claim to be narrower than it is`,
			}
		}
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
// Tunnel defaults to 443 only: a tunnel decision is a decision to relay opaque
// bytes, and defaulting that to every port would make the proxy a generic TCP
// relay. Intercept defaults to any port, because an intercepted connection is
// still parsed and evaluated per request (AC-16).
//
// Deny defaults to any port too, and must. A narrower default than intercept's
// breaks "deny beats intercept regardless of order": an unported deny would
// cover only 443 while the intercept rule it is meant to override covers every
// port, so off 443 the credential would be injected into exactly the path the
// operator denied. A deny is a refusal to reach something, not a statement
// about which port it listens on.
func effectivePorts(r Rule) []int {
	if len(r.Ports) > 0 {
		ports := slices.Clone(r.Ports)
		slices.Sort(ports)
		return slices.Compact(ports)
	}
	if r.Mode == ModeTunnel {
		return []int{DefaultTunnelPort}
	}
	return nil
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
