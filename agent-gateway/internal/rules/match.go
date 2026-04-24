package rules

import "strings"

// ruleset is the immutable compiled representation of a rule list.
// It is stored inside an atomic.Pointer inside Engine and replaced wholesale
// on each successful Reload. Keeping it unexported prevents callers from
// accidentally retaining a stale snapshot.
type ruleset struct {
	rules     []Rule
	hostIndex map[string]map[string]struct{} // agent → set of host-glob patterns
}

// Compile creates a *ruleset from a slice of already-compiled rules.
// The rules are evaluated in the order provided (first-match-wins).
// This is the low-level constructor used by tests and by Engine internally.
func Compile(rs []Rule) *ruleset {
	// Defensive copy so the caller cannot mutate the ruleset's rule list.
	cp := make([]Rule, len(rs))
	copy(cp, rs)
	return &ruleset{
		rules:     cp,
		hostIndex: buildHostsForAgent(cp),
	}
}

// buildHostsForAgent constructs the agent→hosts index from a rule slice.
// Rules with nil Agents (all-agents wildcard) are stored under the special
// sentinel key "" and merged into every agent's result at lookup time.
func buildHostsForAgent(rs []Rule) map[string]map[string]struct{} {
	m := make(map[string]map[string]struct{})
	for i := range rs {
		r := &rs[i]
		host := r.Match.Host
		if host == "" {
			// Defence-in-depth: the HCL parser rejects empty host (see
			// decodeRuleBlock in parse.go). This branch only fires for rules
			// constructed directly via Compile() in tests; skipping them here
			// preserves the previous tunnel behaviour for that narrow path.
			continue
		}
		if r.Agents == nil {
			// nil Agents means "applies to all agents"; store under sentinel "".
			if m[""] == nil {
				m[""] = make(map[string]struct{})
			}
			m[""][host] = struct{}{}
		} else {
			for _, agent := range r.Agents {
				if m[agent] == nil {
					m[agent] = make(map[string]struct{})
				}
				m[agent][host] = struct{}{}
			}
		}
	}
	return m
}

// Evaluate walks the rule list in order and returns the first MatchResult
// whose criteria are satisfied by req. It returns nil if no rule matches.
//
// When a body matcher is bypassed (BodyTruncated or BodyTimedOut), Evaluate
// returns a MatchResult with a non-empty Error field and no verdict so that
// the audit logger can record the bypass reason. The first bypassed rule wins;
// subsequent rules are not evaluated.
func (rs *ruleset) Evaluate(req *Request) *MatchResult {
	for i := range rs.rules {
		r := &rs.rules[i]
		matched, bypassErr := matchRule(r, req)
		if matched {
			return &MatchResult{Rule: r, Index: i}
		}
		if bypassErr != "" {
			// Body matcher was bypassed — surface the reason for audit, but do
			// not return a verdict.
			return &MatchResult{Rule: r, Index: i, Error: bypassErr}
		}
	}
	return nil
}

// hostsForAgent returns the merged set of host-glob patterns for the given
// agent: the agent's own rules plus the all-agents wildcard rules (nil Agents).
func (rs *ruleset) hostsForAgent(agent string) map[string]struct{} {
	out := make(map[string]struct{})
	// Add hosts from rules scoped to this specific agent.
	for h := range rs.hostIndex[agent] {
		out[h] = struct{}{}
	}
	// Add hosts from rules that apply to all agents (nil Agents, stored under "").
	for h := range rs.hostIndex[""] {
		out[h] = struct{}{}
	}
	return out
}

// needsBodyBuffer reports whether any rule that could match the given agent
// and host has a body matcher. It uses a conservative check: host glob must
// match and the rule's agent filter must include agent (or be nil).
func (rs *ruleset) needsBodyBuffer(agent, host string) bool {
	for i := range rs.rules {
		r := &rs.rules[i]
		// Agent filter: nil means all agents.
		if r.Agents != nil {
			found := false
			for _, a := range r.Agents {
				if a == agent {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		// Host glob: empty means any host.
		if r.hostGlob.re != nil && !r.hostGlob.re.MatchString(host) {
			continue
		}
		if r.body != nil {
			return true
		}
	}
	return false
}

// matchRule checks whether every criterion in r is satisfied by req.
// It returns (matched bool, bypassError string). bypassError is non-empty only
// when a body matcher was bypassed due to size or timeout; in that case matched
// is always false. An absent criterion (empty string / nil map) is a wildcard.
func matchRule(r *Rule, req *Request) (matched bool, bypassError string) {
	// Agent filter.
	if r.Agents != nil {
		found := false
		for _, a := range r.Agents {
			if a == req.Agent {
				found = true
				break
			}
		}
		if !found {
			return false, ""
		}
	}

	// Host glob.
	if r.hostGlob.re != nil {
		if !r.hostGlob.re.MatchString(req.Host) {
			return false, ""
		}
	}

	// Path glob.
	//
	// Path matching is case-insensitive. The rule pattern is lowercased at
	// compile time (see compileRule in parse.go); the request path is
	// lowercased here. Upstream services (e.g. many enterprise API gateways)
	// commonly normalise path case before routing, so a deny rule on
	// "/admin/*" that silently missed "/ADMIN/foo" would be a security trap,
	// not a feature. strings.ToLower handles Unicode paths correctly.
	if r.pathGlob.re != nil {
		if !r.pathGlob.re.MatchString(strings.ToLower(req.Path)) {
			return false, ""
		}
	}

	// Method exact match. Both sides are normalised to uppercase — the rule
	// method is uppercased at compile time (see compileRule in parse.go) and
	// the request method is uppercased here. HTTP methods are canonically
	// uppercase (RFC 9110), but clients occasionally send mixed case; a rule
	// that silently misses "post" when the author wrote "POST" is a trap.
	if r.Match.Method != "" {
		if r.Match.Method != strings.ToUpper(req.Method) {
			return false, ""
		}
	}

	// Header regex matches (case-insensitive lookup via http.Header.Get).
	for name, re := range r.headerREs {
		val := req.Header.Get(name)
		if val == "" {
			// Header absent — no match.
			return false, ""
		}
		if !re.MatchString(val) {
			return false, ""
		}
	}

	// Body matcher (evaluated last; may produce a bypass error).
	bodyMatched, bypassErr := matchBody(r, req)
	if bypassErr != "" {
		return false, bypassErr
	}
	if !bodyMatched {
		return false, ""
	}

	return true, ""
}
