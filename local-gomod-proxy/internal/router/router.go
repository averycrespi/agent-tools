package router

import "golang.org/x/mod/module"

// Router classifies module paths as private or public based on GOPRIVATE-style
// glob patterns.
type Router struct {
	patterns string
}

// New returns a Router for the given GOPRIVATE value (comma-separated globs).
func New(patterns string) *Router {
	return &Router{patterns: patterns}
}

// IsPrivate reports whether the module path matches any configured pattern.
func (r *Router) IsPrivate(modulePath string) bool {
	if r.patterns == "" {
		return false
	}
	return module.MatchPrefixPatterns(r.patterns, modulePath)
}
