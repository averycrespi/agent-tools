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
