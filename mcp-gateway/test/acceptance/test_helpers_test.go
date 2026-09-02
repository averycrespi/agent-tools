package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	require.NoError(t, err)
	return root + "/../../.."
}

func frontendDevelopmentModuleRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
}
