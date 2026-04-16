package grants

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ValidateAgainstInputSchema checks that the keys referenced by argSchema
// exist in toolSchema's properties, and that any const values align with
// the declared types. It is a best-effort pre-submit check; the server
// re-compiles and re-validates regardless.
func ValidateAgainstInputSchema(argSchema, toolSchema json.RawMessage) error {
	var tool, arg map[string]any
	if err := json.Unmarshal(toolSchema, &tool); err != nil {
		return fmt.Errorf("parsing tool input schema: %w", err)
	}
	if err := json.Unmarshal(argSchema, &arg); err != nil {
		return fmt.Errorf("parsing arg schema: %w", err)
	}
	toolProps, _ := tool["properties"].(map[string]any)
	return walk(toolProps, arg)
}

func walk(toolProps map[string]any, argSchema map[string]any) error {
	argProps, _ := argSchema["properties"].(map[string]any)
	for key, raw := range argProps {
		toolEntry, ok := toolProps[key].(map[string]any)
		if !ok {
			return fmt.Errorf("unknown arg %q%s", key, suggestion(key, toolProps))
		}
		sub, _ := raw.(map[string]any)
		if cst, has := sub["const"]; has {
			if err := checkType(key, cst, toolEntry["type"]); err != nil {
				return err
			}
		}
		if enums, has := sub["enum"].([]any); has {
			for _, v := range enums {
				if err := checkType(key, v, toolEntry["type"]); err != nil {
					return err
				}
			}
		}
		if _, has := sub["properties"]; has {
			nestedProps, _ := toolEntry["properties"].(map[string]any)
			if err := walk(nestedProps, sub); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkType(key string, v any, declared any) error {
	want, _ := declared.(string)
	if want == "" {
		return nil
	}
	switch want {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("arg %q: type mismatch (want string, got %T)", key, v)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("arg %q: type mismatch (want boolean, got %T)", key, v)
		}
	case "integer", "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("arg %q: type mismatch (want %s, got %T)", key, want, v)
		}
	}
	return nil
}

func suggestion(bad string, toolProps map[string]any) string {
	best := ""
	bestDist := 1 << 30
	var keys []string
	for k := range toolProps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d := levenshtein(bad, k)
		if d < bestDist {
			bestDist = d
			best = k
		}
	}
	if best != "" && bestDist <= 3 {
		return fmt.Sprintf(`; did you mean %q?`, best)
	}
	return ""
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
