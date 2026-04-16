package grants

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ToolGroup holds the parsed --tool name and its associated --arg-* flags.
type ToolGroup struct {
	Tool  string
	Flags []string
}

// SplitByTool separates command-line args into global flags and tool-scoped
// flag groups. The first --tool delimits the boundary between globals and
// groups; every subsequent --tool opens a new group.
func SplitByTool(args []string) ([]string, []ToolGroup, error) {
	var (
		global []string
		groups []ToolGroup
		cur    *ToolGroup
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--tool" {
			if i+1 >= len(args) {
				return nil, nil, errors.New("--tool requires a name")
			}
			groups = append(groups, ToolGroup{Tool: args[i+1]})
			cur = &groups[len(groups)-1]
			i++
			continue
		}
		if cur == nil {
			global = append(global, a)
		} else {
			cur.Flags = append(cur.Flags, a)
		}
	}
	if len(groups) == 0 {
		return nil, nil, errors.New("at least one --tool is required")
	}
	return global, groups, nil
}

// BuildSchema compiles one tool group's flags into a JSON Schema fragment.
// Returns the raw JSON bytes suitable for an Entry's ArgSchema.
func BuildSchema(g ToolGroup) (json.RawMessage, error) {
	if schemaFile := findSchemaFileFlag(g.Flags); schemaFile != "" {
		if hasOtherArgFlags(g.Flags) {
			return nil, errors.New("--arg-schema-file is mutually exclusive with other --arg-* flags")
		}
		return os.ReadFile(schemaFile)
	}

	root := map[string]any{"type": "object", "properties": map[string]any{}}
	required := []string{}

	for i := 0; i < len(g.Flags); i++ {
		flag := g.Flags[i]
		if i+1 >= len(g.Flags) {
			return nil, fmt.Errorf("flag %s requires a value", flag)
		}
		val := g.Flags[i+1]
		i++

		key, rawValue, err := parseKV(val)
		if err != nil {
			return nil, err
		}
		constraint, err := operatorToConstraint(flag, rawValue)
		if err != nil {
			return nil, err
		}

		parent := root
		parts := strings.Split(key, ".")
		for depth, part := range parts {
			props := parent["properties"].(map[string]any)
			if depth == len(parts)-1 {
				props[part] = constraint
				required = appendUnique(required, part)
				break
			}
			child, ok := props[part].(map[string]any)
			if !ok {
				child = map[string]any{"type": "object", "properties": map[string]any{}}
				props[part] = child
			}
			reqRaw, _ := parent["required"].([]any)
			already := false
			for _, v := range reqRaw {
				if v == part {
					already = true
					break
				}
			}
			if !already {
				parent["required"] = append(reqRaw, part)
			}
			parent = child
		}
	}

	if len(required) > 0 {
		existing, _ := root["required"].([]any)
		for _, r := range required {
			appendIfAbsent(&existing, r)
		}
		root["required"] = existing
	}
	return json.Marshal(root)
}

func parseKV(s string) (key string, rawValue string, err error) {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	key = s[:eq]
	if strings.Contains(key, "..") || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
		return "", "", fmt.Errorf("invalid key %q", key)
	}
	return key, s[eq+1:], nil
}

func operatorToConstraint(flag, raw string) (map[string]any, error) {
	switch flag {
	case "--arg-equal":
		return map[string]any{"const": parseLiteral(raw)}, nil
	case "--arg-match":
		return map[string]any{"pattern": raw}, nil
	case "--arg-enum":
		parts := strings.Split(raw, ",")
		vals := make([]any, len(parts))
		for i, p := range parts {
			vals[i] = parseLiteral(p)
		}
		return map[string]any{"enum": vals}, nil
	case "--arg-schema-file":
		return nil, errors.New("--arg-schema-file must stand alone")
	default:
		return nil, fmt.Errorf("unknown flag %q", flag)
	}
}

func parseLiteral(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}

func findSchemaFileFlag(flags []string) string {
	for i, f := range flags {
		if f == "--arg-schema-file" && i+1 < len(flags) {
			return flags[i+1]
		}
	}
	return ""
}

func hasOtherArgFlags(flags []string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "--arg-") && f != "--arg-schema-file" {
			return true
		}
	}
	return false
}

func appendUnique(ss []string, s string) []string {
	for _, v := range ss {
		if v == s {
			return ss
		}
	}
	return append(ss, s)
}

func appendIfAbsent(dst *[]any, v string) {
	for _, s := range *dst {
		if s == v {
			return
		}
	}
	*dst = append(*dst, v)
}
