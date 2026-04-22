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
