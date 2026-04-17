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
// Body matching is not performed here (deferred to Task 15).
func (e *Engine) Evaluate(req *Request) *MatchResult {
	for i := range e.rules {
		r := &e.rules[i]
		if matchRule(r, req) {
			return &MatchResult{Rule: r, Index: i}
		}
	}
	return nil
}

// matchRule returns true when every criterion present in r is satisfied
// by req. An absent criterion (empty string / nil map) is a wildcard.
func matchRule(r *Rule, req *Request) bool {
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
			return false
		}
	}

	// Host glob.
	if r.hostGlob.re != nil {
		if !r.hostGlob.re.MatchString(req.Host) {
			return false
		}
	}

	// Path glob.
	if r.pathGlob.re != nil {
		if !r.pathGlob.re.MatchString(req.Path) {
			return false
		}
	}

	// Method exact match (case-sensitive; callers are expected to uppercase).
	if r.Match.Method != "" {
		if r.Match.Method != req.Method {
			return false
		}
	}

	// Header regex matches (case-insensitive lookup via http.Header.Get).
	for name, re := range r.headerREs {
		val := req.Header.Get(name)
		if val == "" {
			// Header absent — no match.
			return false
		}
		if !re.MatchString(val) {
			return false
		}
	}

	return true
}
