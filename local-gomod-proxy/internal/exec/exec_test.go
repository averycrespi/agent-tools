package exec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSRunner_Run_Success(t *testing.T) {
	r := NewOSRunner()
	out, err := r.Run("echo", "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
}

func TestOSRunner_Run_Error(t *testing.T) {
	r := NewOSRunner()
	_, err := r.Run("false")
	assert.Error(t, err)
}

func TestOSRunner_RunDir_UsesDir(t *testing.T) {
	r := NewOSRunner()
	dir := t.TempDir()
	out, err := r.RunDir(dir, "pwd")
	require.NoError(t, err)
	assert.Contains(t, string(out), dir)
}
