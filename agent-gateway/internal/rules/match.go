package rules

// Engine evaluates a Request against a compiled set of rules using
// first-match-wins semantics.
type Engine struct {
	rules []Rule
}

// Compile creates an Engine from a slice of already-compiled rules.
// The rules are evaluated in the order provided (first-match-wins).
func Compile(rs []Rule) *Engine {
	// Defensive copy so the caller cannot mutate the engine's rule list.
	cp := make([]Rule, len(rs))
	copy(cp, rs)
	return &Engine{rules: cp}
}

// Evaluate walks the rule list in order and returns the first MatchResult
// whose criteria are satisfied by req. It returns nil if no rule matches.
//
// When a body matcher is bypassed (BodyTruncated or BodyTimedOut), Evaluate
// returns a MatchResult with a non-empty Error field and no verdict so that
// the audit logger can record the bypass reason. The first bypassed rule wins;
// subsequent rules are not evaluated.
func (e *Engine) Evaluate(req *Request) *MatchResult {
	for i := range e.rules {
		r := &e.rules[i]
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
	if r.pathGlob.re != nil {
		if !r.pathGlob.re.MatchString(req.Path) {
			return false, ""
		}
	}

	// Method exact match (case-sensitive; callers are expected to uppercase).
	if r.Match.Method != "" {
		if r.Match.Method != req.Method {
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
