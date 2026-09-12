package oauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionOAuthMachineFetchUsesOnlyHardenedRemoteFactory(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	seenFactory := false
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		contents, readErr := os.ReadFile(entry.Name())
		require.NoError(t, readErr)
		source := string(contents)
		if strings.Contains(source, "*remote.Factory") {
			seenFactory = true
		}
		for _, prohibited := range []string{"http.Client{", "http.Transport{", "http.DefaultClient", "http.Get(", "http.Post(", "http.NewRequest("} {
			assert.NotContains(t, source, prohibited, "%s bypasses the hardened remote factory", entry.Name())
		}
	}
	assert.True(t, seenFactory, "OAuth production construction must require the hardened remote factory")
}
