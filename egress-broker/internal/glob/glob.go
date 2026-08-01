// Package glob compiles the project's segment-aware glob patterns to anchored
// regular expressions.
//
// This is the only glob-to-regexp translation in the tool. Host matching
// (via internal/hostnorm), credential host-scope matching, and rule path
// matching all reach it, so rule evaluation and credential scoping can never
// drift apart (D16). The agent-gateway branch this tool draws on shipped three
// copies of this function and carried a code comment admitting the drift risk.
//
// Semantics, with sep as the segment separator ("." for hosts, "/" for paths):
//
//   - "*"  matches any run of characters within a single segment.
//   - "**" matches any number of segments, including none when written as
//     "**<sep>".
//   - Every other character is a literal, including regexp metacharacters.
//
// Patterns are anchored at both ends, so "github.com" does not match
// "github.com.attacker.com".
package glob

import (
	"fmt"
	"regexp"
	"strings"
)

// Glob is a compiled pattern.
type Glob struct {
	pattern string
	re      *regexp.Regexp
}

// Compile compiles pattern with sep as the segment separator.
func Compile(pattern string, sep byte) (*Glob, error) {
	re, err := regexp.Compile(ToRegexp(pattern, sep))
	if err != nil {
		return nil, fmt.Errorf("glob: compile %q: %w", pattern, err)
	}
	return &Glob{pattern: pattern, re: re}, nil
}

// Match reports whether s matches the pattern.
func (g *Glob) Match(s string) bool { return g.re.MatchString(s) }

// String returns the pattern the Glob was compiled from.
func (g *Glob) String() string { return g.pattern }

// ToRegexp translates a glob pattern to an anchored regexp string.
func ToRegexp(pattern string, sep byte) string {
	escapedSep := regexp.QuoteMeta(string(sep))

	var sb strings.Builder
	sb.WriteString("^")

	i := 0
	for i < len(pattern) {
		// "**<sep>" spans zero or more whole segments; a trailing "**" spans
		// the remainder of the string.
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			i += 2
			if i < len(pattern) && pattern[i] == sep {
				sb.WriteString(`(?:.*` + escapedSep + `)?`)
				i++
			} else {
				sb.WriteString(`.*`)
			}
			continue
		}
		if pattern[i] == '*' {
			sb.WriteString(`[^` + escapedSep + `]*`)
			i++
			continue
		}
		sb.WriteString(regexp.QuoteMeta(string(pattern[i])))
		i++
	}

	sb.WriteString("$")
	return sb.String()
}
