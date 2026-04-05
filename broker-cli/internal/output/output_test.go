package output_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/averycrespi/agent-tools/broker-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormat_jsonObject(t *testing.T) {
	result := &client.ToolResult{
		Content: []client.ContentBlock{{Type: "text", Text: `{"pushed": true, "commits": 3}`}},
	}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"pushed": true, "commits": 3}]`, got)
}

func TestFormat_plainText(t *testing.T) {
	result := &client.ToolResult{
		Content: []client.ContentBlock{{Type: "text", Text: "Successfully deleted branch"}},
	}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `["Successfully deleted branch"]`, got)
}

func TestFormat_multipleBlocks(t *testing.T) {
	result := &client.ToolResult{
		Content: []client.ContentBlock{
			{Type: "text", Text: `{"pr": 42}`},
			{Type: "text", Text: `{"checks": "passing"}`},
		},
	}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"pr": 42}, {"checks": "passing"}]`, got)
}

func TestFormat_emptyContent(t *testing.T) {
	result := &client.ToolResult{Content: nil}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, got)
}

func TestFormat_nonTextBlocksIgnored(t *testing.T) {
	// Non-text content blocks (type != "text") are skipped.
	result := &client.ToolResult{
		Content: []client.ContentBlock{
			{Type: "image", Text: ""},
			{Type: "text", Text: "hello"},
		},
	}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `["hello"]`, got)
}
