package downstream

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionDownstreamCannotUsePermissiveHTTPOrSDKTransports(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Dir(current)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		require.NoError(t, readErr)
		for _, prohibited := range []string{"http.DefaultClient", "http.Get(", "http.Post(", "CommandTransport", "Client.Connect", "ClientSession.ListTools"} {
			assert.NotContains(t, string(contents), prohibited, "%s contains prohibited transport path", entry.Name())
		}
	}
}
