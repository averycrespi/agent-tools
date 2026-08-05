package rules_test

import (
	"crypto/tls"
	"net/url"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

func engine(t *testing.T, ft rules.Fallthrough, rs ...rules.Rule) *rules.Engine {
	t.Helper()
	e, err := rules.New(rules.Document{Fallthrough: ft, Rules: rs})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// TestConnectDecision walks all five steps of the CONNECT procedure.
func TestConnectDecision(t *testing.T) {
	interceptRule := rules.Rule{
		Name: "gh", Host: "api.github.com", Path: "/repos/**", Mode: rules.ModeIntercept,
	}
	tunnelRule := rules.Rule{Name: "pinned", Host: "*.pinned-sdk.com", Mode: rules.ModeTunnel}
	denyRule := rules.Rule{Name: "ads", Host: "*.doubleclick.net", Mode: rules.ModeDeny}
	pathDeny := rules.Rule{Name: "no-admin", Host: "api.example.com", Path: "/admin/**", Mode: rules.ModeDeny}

	e := engine(t, rules.FallthroughTunnel, interceptRule, tunnelRule, denyRule, pathDeny)

	cases := []struct {
		name       string
		host       string
		port       int
		wantAction rules.ConnectAction
		wantRule   string
		wantMode   string
	}{
		{
			name: "step 2: an intercept match forces MITM even though it is path-scoped",
			host: "api.github.com", port: 443,
			wantAction: rules.ConnectMITM, wantRule: "gh", wantMode: "intercept",
		},
		{
			name: "step 3: host-only tunnel applies at CONNECT",
			host: "cdn.pinned-sdk.com", port: 443,
			wantAction: rules.ConnectTunnel, wantRule: "pinned", wantMode: "tunnel",
		},
		{
			name: "step 3: host-only deny rejects before any dial",
			host: "ads.doubleclick.net", port: 443,
			wantAction: rules.ConnectDeny, wantRule: "ads", wantMode: "deny",
		},
		{
			name: "step 4: only a path-scoped deny matches, so MITM and decide per request",
			host: "api.example.com", port: 443,
			wantAction: rules.ConnectMITM, wantMode: "implicit-allow",
		},
		{
			name: "step 5: no rule matches, fallthrough tunnel",
			host: "unmatched.example.com", port: 443,
			wantAction: rules.ConnectTunnel, wantMode: "fallthrough",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := e.ConnectDecision(tc.host, tc.port)
			if got.Action != tc.wantAction {
				t.Errorf("Action = %q, want %q", got.Action, tc.wantAction)
			}
			if got.Rule != tc.wantRule {
				t.Errorf("Rule = %q, want %q", got.Rule, tc.wantRule)
			}
			if got.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", got.Mode, tc.wantMode)
			}
		})
	}
}

func TestConnectDecisionFallthroughDeny(t *testing.T) {
	e := engine(t, rules.FallthroughDeny,
		rules.Rule{Name: "pinned", Host: "*.pinned-sdk.com", Mode: rules.ModeTunnel})

	got := e.ConnectDecision("unmatched.example.com", 443)
	if got.Action != rules.ConnectDeny {
		t.Errorf("Action = %q, want deny", got.Action)
	}
	if got.Mode != "fallthrough" {
		t.Errorf("Mode = %q, want fallthrough", got.Mode)
	}
	if !strings.Contains(got.Reason, "fallthrough") {
		t.Errorf("Reason %q should explain that fallthrough denied it", got.Reason)
	}

	// AC-5: a tunnel rule keeps its host un-intercepted even under deny.
	tunnelled := e.ConnectDecision("cdn.pinned-sdk.com", 443)
	if tunnelled.Action != rules.ConnectTunnel {
		t.Errorf("a mode:tunnel rule should still tunnel under fallthrough deny, got %q", tunnelled.Action)
	}
}

// TestPortStripping is AC-16: a rule naming a bare host matches a CONNECT on
// any port the rule admits, because matching never sees a host:port string.
func TestPortStripping(t *testing.T) {
	e := engine(t, rules.FallthroughDeny,
		rules.Rule{Name: "gh", Host: "api.github.com", Mode: rules.ModeIntercept})

	for _, port := range []int{443, 8443, 80, 9999} {
		got := e.ConnectDecision("api.github.com", port)
		if got.Action != rules.ConnectMITM {
			t.Errorf("port %d: Action = %q, want mitm — an intercept rule defaults to any port", port, got.Action)
		}
		if got.Rule != "gh" {
			t.Errorf("port %d: Rule = %q, want gh", port, got.Rule)
		}
	}
}

// TestTunnelPorts is the other half of AC-16: a tunnel decision is limited to
// 443 unless a rule opts a port in, and that limit applies to the fallthrough
// path too.
func TestTunnelPorts(t *testing.T) {
	t.Run("a tunnel rule defaults to 443 only", func(t *testing.T) {
		e := engine(t, rules.FallthroughDeny,
			rules.Rule{Name: "pinned", Host: "cdn.pinned-sdk.com", Mode: rules.ModeTunnel})

		if got := e.ConnectDecision("cdn.pinned-sdk.com", 443); got.Action != rules.ConnectTunnel {
			t.Errorf("port 443: Action = %q, want tunnel", got.Action)
		}
		got := e.ConnectDecision("cdn.pinned-sdk.com", 8443)
		if got.Action != rules.ConnectDeny {
			t.Errorf("port 8443: Action = %q, want deny", got.Action)
		}
		// The rule does not apply to 8443, so the fallthrough policy decides —
		// the rule is not consulted and does not appear in the attribution.
		if got.Mode != "fallthrough" {
			t.Errorf("Mode = %q, want fallthrough: a port-scoped rule that excludes this port simply does not apply", got.Mode)
		}
	})

	// A rule whose ports exclude the request port must not override the
	// fallthrough policy. The operator scoped that rule deliberately; treating
	// a non-matching port as a refusal would ignore what they wrote.
	t.Run("a port-scoped rule does not govern ports it excludes", func(t *testing.T) {
		e := engine(t, rules.FallthroughTunnel,
			rules.Rule{Name: "alt-only", Host: "api.example.com", Ports: []int{8443}, Mode: rules.ModeDeny})

		got := e.ConnectDecision("api.example.com", 443)
		if got.Action != rules.ConnectTunnel {
			t.Errorf("Action = %q, want tunnel: the deny rule is scoped to 8443, so 443 falls through", got.Action)
		}
		if got.Mode != "fallthrough" {
			t.Errorf("Mode = %q, want fallthrough", got.Mode)
		}
		if got.Rule != "" {
			t.Errorf("Rule = %q, want empty: no rule applied", got.Rule)
		}

		// On the port it does cover, the rule applies.
		if denied := e.ConnectDecision("api.example.com", 8443); denied.Action != rules.ConnectDeny {
			t.Errorf("port 8443: Action = %q, want deny from the rule", denied.Action)
		}
	})

	t.Run("an explicit ports list opts a port in", func(t *testing.T) {
		e := engine(t, rules.FallthroughDeny,
			rules.Rule{Name: "pinned", Host: "cdn.pinned-sdk.com", Ports: []int{443, 8443}, Mode: rules.ModeTunnel})

		for _, port := range []int{443, 8443} {
			if got := e.ConnectDecision("cdn.pinned-sdk.com", port); got.Action != rules.ConnectTunnel {
				t.Errorf("port %d: Action = %q, want tunnel", port, got.Action)
			}
		}
		if got := e.ConnectDecision("cdn.pinned-sdk.com", 9999); got.Action != rules.ConnectDeny {
			t.Errorf("port 9999: Action = %q, want deny", got.Action)
		}
	})

	// The fallthrough path carries the same limit. Without it, "fallthrough":
	// "tunnel" would relay any port an agent names.
	t.Run("fallthrough tunnelling is limited to 443", func(t *testing.T) {
		e := engine(t, rules.FallthroughTunnel)

		if got := e.ConnectDecision("unmatched.example.com", 443); got.Action != rules.ConnectTunnel {
			t.Errorf("port 443: Action = %q, want tunnel", got.Action)
		}
		got := e.ConnectDecision("unmatched.example.com", 8443)
		if got.Action != rules.ConnectDeny {
			t.Errorf("port 8443: Action = %q, want deny via the fallthrough port limit", got.Action)
		}
		if !strings.Contains(got.Reason, "443") {
			t.Errorf("Reason %q should name the port limit", got.Reason)
		}
	})
}

// TestPlainHTTPFallthroughIgnoresThePortLimit is AC-16's scope.
//
// The limit bounds tunnelling. A plain HTTP request is parsed, evaluated and
// audited whatever port it names, so applying the limit to it refused every
// unmatched plain HTTP request on the shipped default configuration — port 80
// included, with a message about tunnelling.
func TestPlainHTTPFallthroughIgnoresThePortLimit(t *testing.T) {
	e := engine(t, rules.FallthroughTunnel)

	for _, port := range []int{80, 443, 8080} {
		got := e.PlainHTTPDecision("unmatched.example.com", port)
		if got.Action != rules.ConnectTunnel {
			t.Errorf("port %d: Action = %q, want the fallthrough policy to forward it", port, got.Action)
		}
		if got.Mode != "fallthrough" {
			t.Errorf("port %d: Mode = %q, want fallthrough", port, got.Mode)
		}
	}

	// The fallthrough policy itself still governs.
	denying := engine(t, rules.FallthroughDeny)
	if got := denying.PlainHTTPDecision("unmatched.example.com", 80); got.Action != rules.ConnectDeny {
		t.Errorf("Action = %q, want deny under fallthrough \"deny\"", got.Action)
	}
}

// TestGlobAnchoringInDecisions proves the anchoring property survives into
// the decision path, not just the glob unit tests.
func TestGlobMatchingInDecisions(t *testing.T) {
	e := engine(t, rules.FallthroughDeny,
		rules.Rule{Name: "gh", Host: "github.com", Mode: rules.ModeIntercept},
		rules.Rule{Name: "gh-sub", Host: "*.github.io", Mode: rules.ModeTunnel})

	cases := []struct {
		host string
		want rules.ConnectAction
	}{
		{"github.com", rules.ConnectMITM},
		{"github.com.attacker.com", rules.ConnectDeny},
		{"notgithub.com", rules.ConnectDeny},
		{"attacker-github.com", rules.ConnectDeny},
		{"pages.github.io", rules.ConnectTunnel},
		{"github.io", rules.ConnectDeny},
		{"a.b.github.io", rules.ConnectDeny},
	}
	for _, tc := range cases {
		if got := e.ConnectDecision(tc.host, 443); got.Action != tc.want {
			t.Errorf("ConnectDecision(%q) = %q, want %q", tc.host, got.Action, tc.want)
		}
	}
}

// TestDenyBeatsIntercept is D18. The assertion runs in both rule orders,
// because the whole point is that order must not decide the outcome.
func TestDenyBeatsIntercept(t *testing.T) {
	intercept := rules.Rule{
		Name: "inject-gh", Host: "api.github.com", Path: "/repos/**", Mode: rules.ModeIntercept,
		Inject: &rules.Inject{Set: map[string]string{"Authorization": "Bearer ${cred.gh_bot}"}},
	}
	deny := rules.Rule{
		Name: "no-deletes", Host: "api.github.com", Path: "/repos/**", Method: "DELETE", Mode: rules.ModeDeny,
	}

	for _, tc := range []struct {
		name  string
		rules []rules.Rule
	}{
		{"intercept written first", []rules.Rule{intercept, deny}},
		{"deny written first", []rules.Rule{deny, intercept}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := engine(t, rules.FallthroughDeny, tc.rules...)

			got := e.Evaluate("api.github.com", 443, "DELETE", "/repos/o/r")
			if got.Action != rules.RequestDeny {
				t.Errorf("Action = %q, want deny regardless of rule order", got.Action)
			}
			if got.Rule != "no-deletes" {
				t.Errorf("Rule = %q, want no-deletes", got.Rule)
			}
			if got.Inject != nil {
				t.Error("a denied request must carry no injection")
			}

			// A method the deny rule does not cover still gets injected.
			allowed := e.Evaluate("api.github.com", 443, "POST", "/repos/o/r")
			if allowed.Action != rules.RequestForward {
				t.Errorf("POST Action = %q, want forward", allowed.Action)
			}
			if allowed.Rule != "inject-gh" || allowed.Inject == nil {
				t.Errorf("POST should match the intercept rule and carry its injection, got rule %q inject %v",
					allowed.Rule, allowed.Inject)
			}
		})
	}
}

// TestDenyBeatsInterceptOnEveryPort pins the port half of that invariant.
//
// A deny rule with no "ports" once defaulted to 443 while an intercept rule
// with no "ports" covered every port, so off 443 the deny silently stopped
// applying and the credential was injected into the denied path.
func TestDenyBeatsInterceptOnEveryPort(t *testing.T) {
	e := engine(t, rules.FallthroughDeny,
		rules.Rule{
			Name: "api", Host: "api.example.com", Mode: rules.ModeIntercept,
			Inject: &rules.Inject{Set: map[string]string{"Authorization": "Bearer ${cred.tok}"}},
		},
		rules.Rule{Name: "no-admin", Host: "api.example.com", Path: "/admin/**", Mode: rules.ModeDeny},
	)

	for _, port := range []int{443, 80, 8443, 9999} {
		got := e.Evaluate("api.example.com", port, "GET", "/admin/users")
		if got.Action != rules.RequestDeny {
			t.Errorf("port %d: Action = %q, want deny on every port the intercept rule covers", port, got.Action)
		}
		if got.Inject != nil {
			t.Errorf("port %d: a denied request must carry no injection", port)
		}
	}
}

func TestEvaluateImplicitAllow(t *testing.T) {
	e := engine(t, rules.FallthroughDeny,
		rules.Rule{Name: "no-admin", Host: "api.example.com", Path: "/admin/**", Mode: rules.ModeDeny})

	// A path the deny rule does not cover: forwarded, no injection, and
	// attributed as implicit-allow rather than as a fallthrough, because the
	// host is already known to policy.
	got := e.Evaluate("api.example.com", 443, "GET", "/public/thing")
	if got.Action != rules.RequestForward {
		t.Errorf("Action = %q, want forward", got.Action)
	}
	if got.Mode != "implicit-allow" {
		t.Errorf("Mode = %q, want implicit-allow", got.Mode)
	}
	if got.Inject != nil {
		t.Error("an implicit-allow request must carry no injection")
	}
	if got.Rule != "" {
		t.Errorf("Rule = %q, want empty: no rule matched", got.Rule)
	}

	// The covered path is denied.
	denied := e.Evaluate("api.example.com", 443, "GET", "/admin/users")
	if denied.Action != rules.RequestDeny {
		t.Errorf("Action = %q, want deny", denied.Action)
	}
}

func TestEvaluateMethodMatching(t *testing.T) {
	e := engine(t, rules.FallthroughDeny,
		rules.Rule{Name: "posts-only", Host: "api.example.com", Method: "POST", Mode: rules.ModeIntercept,
			Inject: &rules.Inject{Set: map[string]string{"X-Token": "${cred.t}"}}})

	if got := e.Evaluate("api.example.com", 443, "POST", "/x"); got.Rule != "posts-only" {
		t.Errorf("POST should match, got rule %q", got.Rule)
	}
	// Methods are normalised to upper case at load, so a lower-case rule
	// still matches the wire method.
	if got := e.Evaluate("api.example.com", 443, "GET", "/x"); got.Rule != "" {
		t.Errorf("GET should not match a POST-only rule, got rule %q", got.Rule)
	}
}

func TestEvaluateMethodCaseNormalised(t *testing.T) {
	e := engine(t, rules.FallthroughDeny,
		rules.Rule{Name: "lower", Host: "api.example.com", Method: "post", Mode: rules.ModeIntercept})

	if got := e.Evaluate("api.example.com", 443, "POST", "/x"); got.Rule != "lower" {
		t.Errorf("a lower-case method in a rule should match POST on the wire, got rule %q", got.Rule)
	}
}

func TestEvaluatePathMatching(t *testing.T) {
	e := engine(t, rules.FallthroughDeny,
		rules.Rule{Name: "issues", Host: "api.github.com", Path: "/repos/*/*/issues", Mode: rules.ModeIntercept})

	cases := []struct {
		path string
		want string
	}{
		{"/repos/octocat/hello/issues", "issues"},
		{"/repos/octocat/hello/issues/1", ""},
		{"/repos/octocat/issues", ""},
		{"/other", ""},
	}
	for _, tc := range cases {
		if got := e.Evaluate("api.github.com", 443, "GET", tc.path); got.Rule != tc.want {
			t.Errorf("path %q matched rule %q, want %q", tc.path, got.Rule, tc.want)
		}
	}
}

// TestOverlappingHostOnlyRulesPreferDeny covers globs that overlap without
// being written identically, which load-time validation cannot catch. The
// outcome must not depend on which rule appears first in the file.
func TestOverlappingHostOnlyRulesPreferDeny(t *testing.T) {
	tunnel := rules.Rule{Name: "tunnel-exact", Host: "api.example.com", Mode: rules.ModeTunnel}
	deny := rules.Rule{Name: "deny-wildcard", Host: "*.example.com", Mode: rules.ModeDeny}

	for _, tc := range []struct {
		name  string
		rules []rules.Rule
	}{
		{"tunnel written first", []rules.Rule{tunnel, deny}},
		{"deny written first", []rules.Rule{deny, tunnel}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := engine(t, rules.FallthroughTunnel, tc.rules...)

			got := e.ConnectDecision("api.example.com", 443)
			if got.Action != rules.ConnectDeny {
				t.Errorf("Action = %q, want deny regardless of rule order", got.Action)
			}
			if got.Rule != "deny-wildcard" {
				t.Errorf("Rule = %q, want deny-wildcard", got.Rule)
			}
		})
	}

	// A host only the tunnel rule's glob covers still tunnels.
	e := engine(t, rules.FallthroughDeny, tunnel)
	if got := e.ConnectDecision("api.example.com", 443); got.Action != rules.ConnectTunnel {
		t.Errorf("Action = %q, want tunnel when no deny rule overlaps", got.Action)
	}
}

func TestStoreReloadSwapsAtomically(t *testing.T) {
	initial := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules:       []rules.Rule{{Name: "a", Host: "a.example.com", Mode: rules.ModeDeny}},
	}
	store, err := rules.NewStore(initial)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if got := store.Engine().ConnectDecision("a.example.com", 443); got.Action != rules.ConnectDeny {
		t.Fatalf("before reload: Action = %q, want deny", got.Action)
	}

	next := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules:       []rules.Rule{{Name: "a", Host: "a.example.com", Mode: rules.ModeTunnel}},
	}
	if err := store.Reload(next); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := store.Engine().ConnectDecision("a.example.com", 443); got.Action != rules.ConnectTunnel {
		t.Errorf("after reload: Action = %q, want tunnel", got.Action)
	}
}

// TestStoreReloadKeepsPreviousOnError is the SIGHUP safety property: a typo in
// rules.json must not take the sandbox's network down.
func TestStoreReloadKeepsPreviousOnError(t *testing.T) {
	initial := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules:       []rules.Rule{{Name: "a", Host: "a.example.com", Mode: rules.ModeDeny}},
	}
	store, err := rules.NewStore(initial)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	broken := rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules:       []rules.Rule{{Name: "bad", Host: "*.com", Mode: rules.ModeTunnel}},
	}
	if err := store.Reload(broken); err == nil {
		t.Fatal("Reload of an invalid document = nil, want an error")
	}

	if got := store.Engine().ConnectDecision("a.example.com", 443); got.Action != rules.ConnectDeny {
		t.Errorf("after a failed reload: Action = %q, want the previous ruleset still serving", got.Action)
	}
}

func TestStoreRejectsInvalidInitialDocument(t *testing.T) {
	_, err := rules.NewStore(rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules:       []rules.Rule{{Name: "bad", Host: "*.com", Mode: rules.ModeTunnel}},
	})
	if err == nil {
		t.Error("NewStore with an invalid document = nil, want an error")
	}
}

// TestPathNormalizationDefeatsTraversal is a regression test. Go's URL parsing
// leaves "." and ".." in place, so matching the literal path let a deny rule
// be bypassed by a spelling the upstream would collapse back to the denied
// path before its own routing.
func TestPathNormalizationDefeatsTraversal(t *testing.T) {
	e := engine(t, rules.FallthroughTunnel,
		rules.Rule{Name: "no-admin", Host: "api.example.com", Path: "/admin/**", Mode: rules.ModeDeny})

	bypasses := []string{
		"/admin/secret",
		"/public/../admin/secret",
		"/./admin/secret",
		"//admin/secret",
		"/a/b/../../admin/secret",
		"/admin/./secret",
		"/admin//secret",
		"/../admin/secret",
	}
	for _, p := range bypasses {
		got := e.Evaluate("api.example.com", 443, "GET", p)
		if got.Action != rules.RequestDeny {
			t.Errorf("Evaluate(%q) = %q, want deny: it collapses to a denied path", p, got.Action)
		}
	}

	// A genuinely different path is still allowed, so normalisation has not
	// simply made everything match.
	if got := e.Evaluate("api.example.com", 443, "GET", "/public/thing"); got.Action != rules.RequestForward {
		t.Errorf("a non-denied path = %q, want forward", got.Action)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/admin", "/admin"},
		{"/admin/", "/admin/"},
		{"//admin", "/admin"},
		{"/./admin", "/admin"},
		{"/a/../admin", "/admin"},
		{"/../admin", "/admin"},
		{"/admin//secret", "/admin/secret"},
		{"/admin/./", "/admin/"},
		{"relative/path", "/relative/path"},
	}
	for _, tc := range cases {
		if got := rules.NormalizePath(tc.in); got != tc.want {
			t.Errorf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPathNormalizationPreservesTrailingSlash: "/admin/**" matches "/admin/"
// but not "/admin", so collapsing the trailing slash would change matching.
func TestPathNormalizationPreservesTrailingSlash(t *testing.T) {
	e := engine(t, rules.FallthroughTunnel,
		rules.Rule{Name: "no-admin", Host: "api.example.com", Path: "/admin/**", Mode: rules.ModeDeny})

	if got := e.Evaluate("api.example.com", 443, "GET", "/admin/"); got.Action != rules.RequestDeny {
		t.Errorf(`Evaluate("/admin/") = %q, want deny`, got.Action)
	}
}

// TestPathNormalizationHandlesEncodedTraversal: Go decodes percent-escapes
// into URL.Path before we see it, so the encoded spellings collapse the same
// way. Pinned because matching RawPath instead would silently reopen the gap.
func TestPathNormalizationHandlesEncodedTraversal(t *testing.T) {
	e := engine(t, rules.FallthroughTunnel,
		rules.Rule{Name: "no-admin", Host: "h", Path: "/admin/**", Mode: rules.ModeDeny})

	raws := []string{
		"/%2e%2e/admin/secret",
		"/public/%2e%2e/admin/secret",
		"/%2E%2E/admin/secret",
		"/%2f%2fadmin/secret",
		"/admin%2fsecret",
		"/public/..%2fadmin/secret",
	}
	for _, raw := range raws {
		u, err := url.Parse("https://h" + raw)
		if err != nil {
			t.Fatalf("parsing %q: %v", raw, err)
		}
		if got := e.Evaluate("h", 443, "GET", u.Path); got.Action != rules.RequestDeny {
			t.Errorf("%s (decoded %q) = %q, want deny", raw, u.Path, got.Action)
		}
	}
}

// TestConnectDecisionAllowPrivate covers the grant reaching the dial decision.
func TestConnectDecisionAllowPrivate(t *testing.T) {
	t.Run("set by the matching rule", func(t *testing.T) {
		e := engine(t, "tunnel", rules.Rule{
			Name: "svc-prod", Host: "svc-prod.ds.aws.example.com",
			Mode: rules.ModeIntercept, AllowPrivate: true,
		})
		if got := e.ConnectDecision("svc-prod.ds.aws.example.com", 443); !got.AllowPrivate {
			t.Errorf("AllowPrivate = false, want true for a rule that sets it")
		}
	})

	t.Run("absent by default", func(t *testing.T) {
		e := engine(t, "tunnel", rules.Rule{
			Name: "gh", Host: "api.github.com", Mode: rules.ModeIntercept,
		})
		if got := e.ConnectDecision("api.github.com", 443); got.AllowPrivate {
			t.Errorf("AllowPrivate = true, want false for a rule that does not set it")
		}
	})

	t.Run("absent on a fallthrough decision", func(t *testing.T) {
		e := engine(t, "tunnel")
		if got := e.ConnectDecision("unmatched.example.com", 443); got.AllowPrivate {
			t.Errorf("AllowPrivate = true, want false when no rule matches")
		}
	})

	t.Run("not granted by a port the rule excludes", func(t *testing.T) {
		e := engine(t, "tunnel", rules.Rule{
			Name: "svc-prod", Host: "svc-prod.ds.aws.example.com", Ports: []int{443},
			Mode: rules.ModeIntercept, AllowPrivate: true,
		})
		if got := e.ConnectDecision("svc-prod.ds.aws.example.com", 8443); got.AllowPrivate {
			t.Errorf("AllowPrivate = true, want false on a port the rule excludes")
		}
	})
}

// TestConnectDecisionAllowPrivateIsOrderIndependent is the invariant that
// matters most here: overlapping globs must not make file order decide whether
// a private dial is permitted, exactly as they must not decide deny-vs-tunnel.
func TestConnectDecisionAllowPrivateIsOrderIndependent(t *testing.T) {
	broad := rules.Rule{Name: "broad", Host: "**.aws.example.com", Mode: rules.ModeIntercept}
	narrow := rules.Rule{
		Name: "narrow", Host: "svc-prod.ds.aws.example.com",
		Mode: rules.ModeIntercept, AllowPrivate: true,
	}

	for _, order := range [][]rules.Rule{{broad, narrow}, {narrow, broad}} {
		e := engine(t, "tunnel", order...)
		got := e.ConnectDecision("svc-prod.ds.aws.example.com", 443)
		if !got.AllowPrivate {
			t.Errorf("rules in order [%s %s]: AllowPrivate = false, want true — any matching rule granting it is enough",
				order[0].Name, order[1].Name)
		}
	}
}

// TestConnectDecisionAllowPrivateNotLeakedToOtherHosts pins the grant to the
// glob that carries it: a sibling host matched only by the broad rule gets
// nothing.
func TestConnectDecisionAllowPrivateNotLeakedToOtherHosts(t *testing.T) {
	e := engine(t, "tunnel",
		rules.Rule{Name: "broad", Host: "**.aws.example.com", Mode: rules.ModeIntercept},
		rules.Rule{
			Name: "narrow", Host: "svc-prod.ds.aws.example.com",
			Mode: rules.ModeIntercept, AllowPrivate: true,
		},
	)
	if got := e.ConnectDecision("other.aws.example.com", 443); got.AllowPrivate {
		t.Errorf("AllowPrivate = true for other.aws.example.com, want false: only the narrow rule grants it")
	}
}

// TestConnectDecisionMinTLSVersion covers the floor reaching the transport
// choice, including the default when no rule names one.
func TestConnectDecisionMinTLSVersion(t *testing.T) {
	t.Run("defaults to 1.3 when unset", func(t *testing.T) {
		e := engine(t, "tunnel", rules.Rule{
			Name: "gh", Host: "api.github.com", Mode: rules.ModeIntercept,
		})
		if got := e.ConnectDecision("api.github.com", 443); got.MinTLSVersion != tls.VersionTLS13 {
			t.Errorf("MinTLSVersion = %#x, want TLS 1.3 (%#x)", got.MinTLSVersion, tls.VersionTLS13)
		}
	})

	t.Run("set by the matching rule", func(t *testing.T) {
		e := engine(t, "tunnel", rules.Rule{
			Name: "svc-prod", Host: "svc-prod.ds.aws.example.com", Mode: rules.ModeIntercept,
			MinTLSVersion: "1.2",
		})
		if got := e.ConnectDecision("svc-prod.ds.aws.example.com", 443); got.MinTLSVersion != tls.VersionTLS12 {
			t.Errorf("MinTLSVersion = %#x, want TLS 1.2 (%#x)", got.MinTLSVersion, tls.VersionTLS12)
		}
	})

	t.Run("defaults to 1.3 on a fallthrough decision", func(t *testing.T) {
		e := engine(t, "tunnel")
		if got := e.ConnectDecision("unmatched.example.com", 443); got.MinTLSVersion != tls.VersionTLS13 {
			t.Errorf("MinTLSVersion = %#x, want TLS 1.3 (%#x)", got.MinTLSVersion, tls.VersionTLS13)
		}
	})
}

// TestConnectDecisionMinTLSVersionIsOrderIndependent pins the same property
// allow_private has: with overlapping globs, the lowest floor among matching
// rules wins, so reordering the file cannot change what is negotiated.
func TestConnectDecisionMinTLSVersionIsOrderIndependent(t *testing.T) {
	strict := rules.Rule{Name: "broad", Host: "**.aws.example.com", Mode: rules.ModeIntercept}
	relaxed := rules.Rule{
		Name: "narrow", Host: "svc-prod.ds.aws.example.com", Mode: rules.ModeIntercept,
		MinTLSVersion: "1.2",
	}

	for _, order := range [][]rules.Rule{{strict, relaxed}, {relaxed, strict}} {
		e := engine(t, "tunnel", order...)
		got := e.ConnectDecision("svc-prod.ds.aws.example.com", 443)
		if got.MinTLSVersion != tls.VersionTLS12 {
			t.Errorf("rules in order [%s %s]: MinTLSVersion = %#x, want TLS 1.2 — the lowest matching floor wins",
				order[0].Name, order[1].Name, got.MinTLSVersion)
		}
	}
}

// TestConnectDecisionMinTLSVersionNotLeakedToOtherHosts keeps the relaxation on
// the glob that carries it.
func TestConnectDecisionMinTLSVersionNotLeakedToOtherHosts(t *testing.T) {
	e := engine(t, "tunnel",
		rules.Rule{Name: "broad", Host: "**.aws.example.com", Mode: rules.ModeIntercept},
		rules.Rule{
			Name: "narrow", Host: "svc-prod.ds.aws.example.com", Mode: rules.ModeIntercept,
			MinTLSVersion: "1.2",
		},
	)
	if got := e.ConnectDecision("other.aws.example.com", 443); got.MinTLSVersion != tls.VersionTLS13 {
		t.Errorf("MinTLSVersion = %#x for other.aws.example.com, want the 1.3 default", got.MinTLSVersion)
	}
}
