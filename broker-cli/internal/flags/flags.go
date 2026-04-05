package flags

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const paramFlag = "raw-field"
const rawInputFlag = "raw-input"

// AddSchemaFlags adds cobra flags derived from a JSON Schema to cmd.
// Also adds --param and --raw-input for complex types.
func AddSchemaFlags(cmd *cobra.Command, schema map[string]any) {
	required, _ := schema["required"].([]any)
	requiredSet := make(map[string]bool, len(required))
	for _, r := range required {
		if s, ok := r.(string); ok {
			requiredSet[s] = true
		}
	}

	props, _ := schema["properties"].(map[string]any)
	for name, def := range props {
		d, _ := def.(map[string]any)
		typ, _ := d["type"].(string)
		desc, _ := d["description"].(string)
		if requiredSet[name] {
			desc += " (required)"
		}
		flagName := strings.ReplaceAll(name, "_", "-")
		switch typ {
		case "string":
			cmd.Flags().String(flagName, "", desc)
		case "boolean":
			cmd.Flags().Bool(flagName, false, desc)
		case "integer", "number":
			cmd.Flags().Int64(flagName, 0, desc)
			// object/array/unknown: handled via --param
		}
	}
	cmd.Flags().StringArray(paramFlag, nil, "Set a field as raw JSON: --raw-field 'key=value'")
	cmd.Flags().String(rawInputFlag, "", "Pass entire input as a JSON object, bypassing flags")
}

// BuildArgs reads flag values from cmd and returns a map[string]any for the broker.
// --raw-input takes precedence over all other flags.
// --param overrides individual fields.
// Required fields are validated unless --raw-input is used.
func BuildArgs(cmd *cobra.Command, schema map[string]any) (map[string]any, error) {
	// --raw-input bypasses everything
	if raw, err := cmd.Flags().GetString(rawInputFlag); err == nil && raw != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return nil, fmt.Errorf("invalid --raw-input JSON: %w", err)
		}
		return args, nil
	}

	props, _ := schema["properties"].(map[string]any)
	args := make(map[string]any)

	for name, def := range props {
		d, _ := def.(map[string]any)
		typ, _ := d["type"].(string)
		flagName := strings.ReplaceAll(name, "_", "-")
		f := cmd.Flags().Lookup(flagName)
		if f == nil || !f.Changed {
			continue
		}
		switch typ {
		case "string":
			v, _ := cmd.Flags().GetString(flagName)
			args[name] = v
		case "boolean":
			v, _ := cmd.Flags().GetBool(flagName)
			args[name] = v
		case "integer", "number":
			v, _ := cmd.Flags().GetInt64(flagName)
			args[name] = v
		}
	}

	// Apply --param overrides
	params, _ := cmd.Flags().GetStringArray(paramFlag)
	for _, p := range params {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			return nil, fmt.Errorf("invalid --raw-field %q: expected key=value", p)
		}
		key := p[:eq]
		val := p[eq+1:]
		var parsed any
		if err := json.Unmarshal([]byte(val), &parsed); err != nil {
			return nil, fmt.Errorf("invalid JSON in --param %q: %w", key, err)
		}
		args[key] = parsed
	}

	// Validate required fields
	required, _ := schema["required"].([]any)
	var missing []string
	for _, r := range required {
		name, _ := r.(string)
		if _, ok := args[name]; !ok {
			flagName := strings.ReplaceAll(name, "_", "-")
			desc := ""
			if d, ok := props[name].(map[string]any); ok {
				desc, _ = d["description"].(string)
			}
			if desc != "" {
				missing = append(missing, fmt.Sprintf("--%s (%s)", flagName, desc))
			} else {
				missing = append(missing, "--"+flagName)
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required flags: %s; use --help to see all flags or --raw-input to pass the full input as JSON",
			strings.Join(missing, ", "))
	}

	return args, nil
}
