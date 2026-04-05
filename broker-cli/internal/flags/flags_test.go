package flags_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/broker-cli/internal/flags"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCmd(schema map[string]any) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	flags.AddSchemaFlags(cmd, schema)
	return cmd
}

func parse(cmd *cobra.Command, args ...string) error {
	return cmd.ParseFlags(args)
}

func TestStringFlag_set(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"remote": map[string]any{"type": "string", "description": "Remote name"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--remote", "origin"))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"remote": "origin"}, args)
}

func TestBoolFlag_set(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"force": map[string]any{"type": "boolean", "description": "Force push"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--force"))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"force": true}, args)
}

func TestIntFlag_set(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"limit": map[string]any{"type": "integer", "description": "Limit"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--limit", "10"))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"limit": int64(10)}, args)
}

func TestRequiredValidation_missing(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"remote": map[string]any{"type": "string"},
		},
		"required": []any{"remote"},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd))
	_, err := flags.BuildArgs(cmd, schema)
	assert.ErrorContains(t, err, "missing required flag: --remote")
}

func TestParamFlag_overridesField(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"items": map[string]any{"type": "array"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--param", `items=["a","b"]`))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b"}, args["items"])
}

func TestRawInput_bypasses(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"remote": map[string]any{"type": "string"},
		},
		"required": []any{"remote"},
	}
	cmd := makeCmd(schema)
	// raw-input bypasses required validation entirely
	require.NoError(t, parse(cmd, "--raw-input", `{"remote":"origin"}`))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"remote": "origin"}, args)
}

func TestUnderscoreFlag_normalizedToHyphen(t *testing.T) {
	// Schema key "repo_path" → flag "--repo-path"; args map key stays "repo_path".
	schema := map[string]any{
		"properties": map[string]any{
			"repo_path": map[string]any{"type": "string", "description": "Path to repo"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--repo-path", "/tmp/repo"))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"repo_path": "/tmp/repo"}, args)
}

func TestUnsetOptional_omitted(t *testing.T) {
	// Optional string flags that are not set should not appear in args.
	schema := map[string]any{
		"properties": map[string]any{
			"branch": map[string]any{"type": "string"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	_, exists := args["branch"]
	assert.False(t, exists)
}
