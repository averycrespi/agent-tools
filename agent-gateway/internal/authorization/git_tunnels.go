package authorization

// ReleaseOpaque settles an already-confirmed tunnel owner after stream and
// completion cleanup. Caller timeout alone never retires an actual owner.
func (r *Repository) ReleaseOpaque(candidate *HTTPEvaluationCandidate) {
	if candidate == nil || candidate.repository != r || candidate.opaqueOrigin == "" || !candidate.opaqueReleased.CompareAndSwap(false, true) {
		return
	}
	r.authority.mu.Lock()
	defer r.authority.mu.Unlock()
	if r.authority.opaqueGitOrigins[candidate.opaqueOrigin] > 1 {
		r.authority.opaqueGitOrigins[candidate.opaqueOrigin]--
	} else {
		delete(r.authority.opaqueGitOrigins, candidate.opaqueOrigin)
	}
}
