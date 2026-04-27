package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// pathSegment is one step of a compiled path. Exactly one of (key, index) is
// meaningful: index == -1 means "key segment", otherwise it is an array index.
type pathSegment struct {
	key   string
	index int
}

// argMatcher matches a stringified value against a pattern.
type argMatcher interface {
	match(value string) bool
}

type exactMatcher struct{ want string }

func (m exactMatcher) match(v string) bool { return v == m.want }

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) match(v string) bool { return m.re.MatchString(v) }

// compiledPattern is one validated arg constraint.
type compiledPattern struct {
	segments []pathSegment
	matcher  argMatcher
}

// parsePath parses a dotted path into segments. Numeric segments become array
// indices; non-numeric segments are keys. Empty paths and empty segments are
// rejected.
func parsePath(p string) ([]pathSegment, error) {
	if p == "" {
		return nil, fmt.Errorf("empty path")
	}
	parts := strings.Split(p, ".")
	segs := make([]pathSegment, 0, len(parts))
	for i, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("empty segment at position %d", i)
		}
		if n, err := strconv.Atoi(part); err == nil {
			segs = append(segs, pathSegment{index: n})
			continue
		}
		segs = append(segs, pathSegment{key: part, index: -1})
	}
	return segs, nil
}

// resolvePath walks args along segments and returns the leaf value plus ok.
// Missing keys, out-of-range indices, and type mismatches all return ok=false.
func resolvePath(args map[string]any, segs []pathSegment) (any, bool) {
	var cur any = args
	for _, s := range segs {
		if s.index < 0 {
			// key segment
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := m[s.key]
			if !ok {
				return nil, false
			}
			cur = v
		} else {
			// index segment
			arr, ok := cur.([]any)
			if !ok {
				return nil, false
			}
			if s.index < 0 || s.index >= len(arr) {
				return nil, false
			}
			cur = arr[s.index]
		}
	}
	return cur, true
}

// stringifyValue converts a resolved JSON value to the string form the matcher
// sees. Plain strings are returned without surrounding quotes; everything else
// goes through json.Marshal so 42 -> "42", true -> "true", null -> "null", and
// objects/arrays produce their compact JSON form.
func stringifyValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// decodeMatcher turns a RawMessage `match` field into an argMatcher.
// Accepts either a JSON string (exact) or {"regex": "<pattern>"}.
func decodeMatcher(raw json.RawMessage) (argMatcher, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("missing match")
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return exactMatcher{want: s}, nil
	}
	// Try regex object: must have exactly one key, "regex", with a string value.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("match must be a string or {\"regex\": \"...\"}: %w", err)
	}
	if len(obj) != 1 {
		return nil, fmt.Errorf("match object must have exactly one key (regex)")
	}
	rxRaw, ok := obj["regex"]
	if !ok {
		return nil, fmt.Errorf("match object key must be \"regex\"")
	}
	var pattern string
	if err := json.Unmarshal(rxRaw, &pattern); err != nil {
		return nil, fmt.Errorf("regex value must be a string: %w", err)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compiling regex %q: %w", pattern, err)
	}
	return regexMatcher{re: re}, nil
}

// matchValue resolves segs against args, stringifies the leaf, and runs matcher.
// Any failure (path miss, type mismatch) returns false.
func (p compiledPattern) matchValue(args map[string]any) bool {
	v, ok := resolvePath(args, p.segments)
	if !ok {
		return false
	}
	return p.matcher.match(stringifyValue(v))
}
