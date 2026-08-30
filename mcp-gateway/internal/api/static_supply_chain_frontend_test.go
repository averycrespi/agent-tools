//go:build frontend

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticSupplyChain(t *testing.T) {
	productionFiles, lock := productionFrontendInputs(t)
	require.NoError(t, validateProductionFrontendSeparation(productionFiles, lock))

	staticRoot := "static"
	entries, err := os.ReadDir(staticRoot)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
		info, infoErr := entry.Info()
		require.NoError(t, infoErr)
		assert.True(t, info.Mode().IsRegular(), entry.Name())
		assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), entry.Name())
	}
	assert.Equal(t, []string{"app.css", "app.js", "index.html"}, names)

	index, err := os.ReadFile(filepath.Join(staticRoot, "index.html"))
	require.NoError(t, err)
	html := string(index)
	assert.Equal(t, 1, strings.Count(html, "<script"))
	assert.Equal(t, 1, strings.Count(html, "<link"))
	for _, forbidden := range []string{"https:", "http:", "data:", "blob:", "<style", "integrity=", "sourceMappingURL="} {
		assert.NotContains(t, html, forbidden)
	}
	for _, name := range []string{"app.js", "app.css"} {
		contents, readErr := os.ReadFile(filepath.Join(staticRoot, name))
		require.NoError(t, readErr)
		for _, forbidden := range []string{"sourceMappingURL=", "navigator.serviceWorker", "serviceWorker.register", "new Function(", "eval("} {
			assert.NotContains(t, string(contents), forbidden, name)
		}
	}

	vite, err := os.ReadFile(filepath.Join("..", "..", "web", "vite.config.ts"))
	require.NoError(t, err)
	for _, required := range []string{"cssCodeSplit: false", "sourcemap: false", "codeSplitting: false", `entryFileNames: "app.js"`, `chunkFileNames: "forbidden-[name].js"`} {
		assert.Contains(t, string(vite), required)
	}
	handlerSource, err := os.ReadFile("handler.go")
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(handlerSource), "//go:embed static/*"))
	assert.NotContains(t, string(handlerSource), "os.ReadFile")
}
