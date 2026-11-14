package paths

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHistoricalInitializationInputRecognitionPreservesBytes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := AcquireInitialization(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	input := []byte(`{"format":"agent-gateway-config","version":1,"retained":"not executable input"}`)
	plan := InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW", ImportDigest: fmt.Sprintf("%x", sha256.Sum256(input))}
	require.NoError(t, BeginInitialization(owner, plan))
	path := filepath.Join(root, InitializationName, "input")
	require.NoError(t, os.WriteFile(path, input, 0600))
	for range 2 {
		actual, complete, err := ReadInitialization(owner.Layout())
		require.NoError(t, err)
		require.False(t, complete)
		require.Equal(t, plan, actual)
		data, err := ReadInitializationInput(owner.Layout(), plan)
		require.NoError(t, err)
		require.Equal(t, input, data)
		data, err = os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, input, data)
	}
	require.NoError(t, os.WriteFile(path, []byte("changed"), 0600))
	_, err = ReadInitializationInput(owner.Layout(), plan)
	require.ErrorIs(t, err, ErrUnsafePath)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "changed", string(actual))
}
