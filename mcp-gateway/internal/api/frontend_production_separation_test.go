package api

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionFrontendSeparationValidator(t *testing.T) {
	files, lock := productionFrontendInputs(t)
	require.NoError(t, validateProductionFrontendSeparation(files, lock))

	for _, test := range []struct {
		name string
		path string
		text string
	}{
		{name: "development import", path: "src/main.tsx", text: `import "../dev-server.ts";`},
		{name: "development contract", path: "src/main.tsx", text: `import "../generated/development-contract.ts";`},
		{name: "development build input", path: "vite.config.ts", text: `input: resolve(webRoot, "dev-server.ts")`},
		{name: "HMR client", path: "static/app.js", text: `import "/@vite/client";`},
	} {
		t.Run(test.name, func(t *testing.T) {
			contaminated := make(map[string][]byte, len(files))
			for path, contents := range files {
				contaminated[path] = append([]byte(nil), contents...)
			}
			contaminated[test.path] = append(contaminated[test.path], test.text...)
			assert.Error(t, validateProductionFrontendSeparation(contaminated, lock))
		})
	}

	changedLock := append([]byte(nil), lock...)
	changedLock = append(changedLock, '\n')
	assert.Error(t, validateProductionFrontendSeparation(files, changedLock))
}

func validateProductionFrontendSeparation(files map[string][]byte, lock []byte) error {
	const expectedLockSHA256 = "3179b01fceb75d1df80217849de915f024e98ee0ab1b821639fbf04b33c5a8e0"
	if digest := fmt.Sprintf("%x", sha256.Sum256(lock)); digest != expectedLockSHA256 {
		return fmt.Errorf("frontend lockfile digest changed: %s", digest)
	}
	config := string(files["vite.config.ts"])
	if !strings.Contains(config, `input: resolve(webRoot, "index.html")`) {
		return fmt.Errorf("production build input changed")
	}
	for path, contents := range files {
		for _, forbidden := range []string{"dev-server", "dev-proxy", "development-contract", "@vite/client", "vite/client", "mcp-gateway-ui-development"} {
			if strings.Contains(string(contents), forbidden) {
				return fmt.Errorf("production frontend %s contains development reference %q", path, forbidden)
			}
		}
	}
	return nil
}

func productionFrontendInputs(t *testing.T) (map[string][]byte, []byte) {
	t.Helper()
	moduleRoot := filepath.Clean(filepath.Join("..", ".."))
	repositoryRoot := filepath.Join(moduleRoot, "..")
	webRoot := filepath.Join(moduleRoot, "web")
	files := make(map[string][]byte)
	for _, relative := range []string{"index.html", "vite.config.ts"} {
		contents, err := os.ReadFile(filepath.Join(webRoot, relative))
		require.NoError(t, err)
		files[relative] = contents
	}
	require.NoError(t, filepath.Walk(filepath.Join(webRoot, "src"), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(webRoot, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = contents
		return nil
	}))
	for _, relative := range []string{"static/index.html", "static/app.js", "static/app.css"} {
		contents, err := os.ReadFile(filepath.Join(moduleRoot, "internal", "api", relative))
		require.NoError(t, err)
		files[relative] = contents
	}
	lock, err := os.ReadFile(filepath.Join(repositoryRoot, "package-lock.json"))
	require.NoError(t, err)
	return files, lock
}
