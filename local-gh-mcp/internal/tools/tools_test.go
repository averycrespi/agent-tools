package tools

import (
	"testing"

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
