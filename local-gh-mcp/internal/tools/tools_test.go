package tools

import (
	"context"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnnotationPresets_ReadOnly(t *testing.T) {
	a := annRead
	require.NotNil(t, a.ReadOnlyHint)
	assert.True(t, *a.ReadOnlyHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
	assert.Nil(t, a.DestructiveHint)
	assert.Nil(t, a.IdempotentHint)
}

func TestAnnotationPresets_Additive(t *testing.T) {
	a := annAdditive
	require.NotNil(t, a.DestructiveHint)
	assert.False(t, *a.DestructiveHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
	assert.Nil(t, a.ReadOnlyHint)
}

func TestAnnotationPresets_Idempotent(t *testing.T) {
	a := annIdempotent
	require.NotNil(t, a.IdempotentHint)
	assert.True(t, *a.IdempotentHint)
	require.NotNil(t, a.DestructiveHint)
	assert.False(t, *a.DestructiveHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
}

func TestAnnotationPresets_Destructive(t *testing.T) {
	a := annDestructive
	require.NotNil(t, a.DestructiveHint)
	assert.True(t, *a.DestructiveHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
	assert.Nil(t, a.ReadOnlyHint)
}

func TestEveryToolHasOpenWorldHint(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.Tools()
	require.NotEmpty(t, tools)
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			require.NotNilf(t, tool.Annotations.OpenWorldHint,
				"tool %s must set OpenWorldHint", tool.Name)
			assert.Truef(t, *tool.Annotations.OpenWorldHint,
				"tool %s: OpenWorldHint must be true (all tools touch GitHub)", tool.Name)
		})
	}
}

func TestEveryLimitParamDeclaresDefault(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.Tools() {
		prop, ok := tool.InputSchema.Properties["limit"].(map[string]any)
		if !ok {
			continue
		}
		t.Run(tool.Name+"/limit", func(t *testing.T) {
			def, ok := prop["default"]
			require.True(t, ok, "tool %s: limit must declare a default", tool.Name)
			switch v := def.(type) {
			case int:
				assert.Equal(t, 30, v)
			case float64:
				assert.Equal(t, float64(30), v)
			default:
				t.Fatalf("tool %s: limit default wrong type %T", tool.Name, def)
			}
		})
	}
}

func TestEveryMaxBodyLengthParamDeclaresDefault(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.Tools() {
		prop, ok := tool.InputSchema.Properties["max_body_length"].(map[string]any)
		if !ok {
			continue
		}
		t.Run(tool.Name+"/max_body_length", func(t *testing.T) {
			def, ok := prop["default"]
			require.True(t, ok, "tool %s: max_body_length must declare a default", tool.Name)
			switch v := def.(type) {
			case int:
				assert.Equal(t, 2000, v)
			case float64:
				assert.Equal(t, float64(2000), v)
			default:
				t.Fatalf("tool %s: max_body_length default wrong type %T", tool.Name, def)
			}
		})
	}
}

func TestNoBareIDParameters(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	banned := []string{"number", "id"}
	for _, tool := range h.Tools() {
		for _, key := range banned {
			_, exists := tool.InputSchema.Properties[key]
			require.Falsef(t, exists,
				"tool %q exposes bare %q property; use a resource-qualified name (pr_number, issue_number, run_id, cache_id)",
				tool.Name, key)
		}
	}
}

func TestEveryNumberParamDeclaresMinimum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	keys := []string{"pr_number", "issue_number"}
	for _, tool := range h.Tools() {
		for _, key := range keys {
			prop, ok := tool.InputSchema.Properties[key].(map[string]any)
			if !ok {
				continue
			}
			t.Run(tool.Name+"/"+key, func(t *testing.T) {
				min, ok := prop["minimum"]
				require.Truef(t, ok, "tool %s: %s must declare minimum", tool.Name, key)
				switch v := min.(type) {
				case int:
					assert.Equal(t, 1, v)
				case float64:
					assert.Equal(t, float64(1), v)
				default:
					t.Fatalf("tool %s: %s minimum wrong type %T", tool.Name, key, min)
				}
			})
		}
	}
}

func TestValidateEnum(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		allowed []string
		wantErr bool
	}{
		{"empty passes", "", []string{"a", "b"}, false},
		{"valid", "a", []string{"a", "b"}, false},
		{"invalid", "c", []string{"a", "b"}, true},
		{"case sensitive", "A", []string{"a"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateEnum("k", tt.value, tt.allowed)
			if tt.wantErr {
				require.NotNil(t, got)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestHandlerRejectsInvalidEnum(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"list_issues_state", "gh_list_issues", map[string]any{"owner": "o", "repo": "r", "state": "bogus"}},
		{"list_prs_state", "gh_list_prs", map[string]any{"owner": "o", "repo": "r", "state": "bogus"}},
		{"list_runs_status", "gh_list_runs", map[string]any{"owner": "o", "repo": "r", "status": "bogus"}},
		{"list_caches_sort", "gh_list_caches", map[string]any{"owner": "o", "repo": "r", "sort": "bogus"}},
		{"list_caches_order", "gh_list_caches", map[string]any{"owner": "o", "repo": "r", "order": "bogus"}},
		{"list_caches_order_without_sort", "gh_list_caches", map[string]any{"owner": "o", "repo": "r", "order": "asc"}},
		{"search_prs_state", "gh_search_prs", map[string]any{"query": "x", "state": "bogus"}},
		{"search_issues_state", "gh_search_issues", map[string]any{"query": "x", "state": "bogus"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(&mockGHClient{})
			req := gomcp.CallToolRequest{}
			req.Params.Name = tt.tool
			req.Params.Arguments = tt.args
			result, err := h.Handle(context.Background(), req)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.IsError, "expected error result for invalid enum input")
		})
	}
}

func TestRequirePositiveInt(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"missing", map[string]any{}, true},
		{"zero", map[string]any{"pr_number": float64(0)}, true},
		{"negative", map[string]any{"pr_number": float64(-5)}, true},
		{"negative_int", map[string]any{"pr_number": -1}, true},
		{"positive", map[string]any{"pr_number": float64(7)}, false},
		{"positive_int", map[string]any{"pr_number": 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, errResult := requirePositiveInt(tt.args, "pr_number")
			if tt.wantErr {
				require.NotNil(t, errResult, "expected error result")
				assert.Equal(t, 0, n)
			} else {
				assert.Nil(t, errResult)
				assert.Greater(t, n, 0)
			}
		})
	}
}
