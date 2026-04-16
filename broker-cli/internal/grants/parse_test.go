package grants

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitByToolBoundaries(t *testing.T) {
	args := []string{
		"--ttl", "1h",
		"--description", "push feat/foo",
		"--tool", "git.git_push",
		"--arg-equal", "branch=feat/foo",
		"--arg-equal", "force=false",
		"--tool", "git.git_fetch",
	}
	global, groups, err := SplitByTool(args)
	require.NoError(t, err)
	require.Equal(t, []string{"--ttl", "1h", "--description", "push feat/foo"}, global)
	require.Len(t, groups, 2)
	require.Equal(t, "git.git_push", groups[0].Tool)
	require.Equal(t, []string{"--arg-equal", "branch=feat/foo", "--arg-equal", "force=false"}, groups[0].Flags)
	require.Equal(t, "git.git_fetch", groups[1].Tool)
	require.Empty(t, groups[1].Flags)
}

func TestSplitByToolNoTool(t *testing.T) {
	_, _, err := SplitByTool([]string{"--ttl", "1h"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one --tool")
}

func TestSplitByToolMissingName(t *testing.T) {
	_, _, err := SplitByTool([]string{"--tool"})
	require.Error(t, err)
}

func TestBuildSchema_AllOperators(t *testing.T) {
	group := ToolGroup{
		Tool: "git.git_push",
		Flags: []string{
			"--arg-equal", "branch=feat/foo",
			"--arg-equal", "force=false",
			"--arg-match", "tag=^v[0-9]+$",
			"--arg-enum", "remote=origin,upstream",
		},
	}
	schema, err := BuildSchema(group)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(schema, &decoded))

	require.Equal(t, "object", decoded["type"])
	props := decoded["properties"].(map[string]any)
	require.Equal(t, "feat/foo", props["branch"].(map[string]any)["const"])
	require.Equal(t, false, props["force"].(map[string]any)["const"])
	require.Equal(t, "^v[0-9]+$", props["tag"].(map[string]any)["pattern"])
	require.ElementsMatch(t, []any{"origin", "upstream"}, props["remote"].(map[string]any)["enum"])

	required := decoded["required"].([]any)
	require.ElementsMatch(t, []any{"branch", "force", "tag", "remote"}, required)
}

func TestBuildSchema_DotPathNesting(t *testing.T) {
	group := ToolGroup{
		Tool:  "x.y",
		Flags: []string{"--arg-equal", "config.max_retries=3"},
	}
	schema, err := BuildSchema(group)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(schema, &decoded))
	props := decoded["properties"].(map[string]any)
	config := props["config"].(map[string]any)
	require.Equal(t, "object", config["type"])
	nested := config["properties"].(map[string]any)
	require.EqualValues(t, 3, nested["max_retries"].(map[string]any)["const"])
}

func TestBuildSchema_SchemaFileExclusive(t *testing.T) {
	group := ToolGroup{
		Tool: "x.y",
		Flags: []string{
			"--arg-schema-file", "foo.json",
			"--arg-equal", "branch=main",
		},
	}
	_, err := BuildSchema(group)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--arg-schema-file is mutually exclusive")
}
