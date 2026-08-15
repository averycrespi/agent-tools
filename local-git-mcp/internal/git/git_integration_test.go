//go:build integration

package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	execrunner "github.com/averycrespi/agent-tools/local-git-mcp/internal/exec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runGitIntegration(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func runGitIntegrationError(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	_, err := cmd.CombinedOutput()
	return err
}

func initBareIntegrationRepo(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(path, 0o755))
	runGitIntegration(t, path, "init", "--bare")
	return path
}

func initWorkingIntegrationRepo(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(path, 0o755))
	runGitIntegration(t, path, "init", "-b", "main")
	runGitIntegration(t, path, "config", "user.name", "Integration Test")
	runGitIntegration(t, path, "config", "user.email", "integration@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(path, "content.txt"), []byte("initial\n"), 0o600))
	runGitIntegration(t, path, "add", "content.txt")
	runGitIntegration(t, path, "commit", "-m", "initial")
	return path
}

func integrationClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient(execrunner.NewOSRunner(), nil, true)
	require.NoError(t, err)
	return client
}

func TestIntegrationPush_VerifiesAndPushesBranchAndTag(t *testing.T) {
	root := t.TempDir()
	work := initWorkingIntegrationRepo(t, root, "work")
	remote := initBareIntegrationRepo(t, root, "remote.git")
	runGitIntegration(t, work, "remote", "add", "origin", remote)
	client := integrationClient(t)

	_, err := client.Push(t.Context(), work, "origin", remote, "refs/heads/main", "refs/heads/main", false)
	require.NoError(t, err)
	assert.Equal(t, runGitIntegration(t, work, "rev-parse", "refs/heads/main"), runGitIntegration(t, remote, "rev-parse", "refs/heads/main"))

	runGitIntegration(t, work, "tag", "v1")
	_, err = client.Push(t.Context(), work, "origin", remote, "refs/tags/v1", "refs/tags/v1", false)
	require.NoError(t, err)
	assert.Equal(t, runGitIntegration(t, work, "rev-parse", "refs/tags/v1"), runGitIntegration(t, remote, "rev-parse", "refs/tags/v1"))
}

func TestIntegrationRemoteURLs_SelectPushVersusFetchDestinations(t *testing.T) {
	root := t.TempDir()
	work := initWorkingIntegrationRepo(t, root, "work")
	fetchRemote := initBareIntegrationRepo(t, root, "fetch.git")
	pushRemote := initBareIntegrationRepo(t, root, "push.git")
	runGitIntegration(t, work, "remote", "add", "origin", fetchRemote)
	runGitIntegration(t, work, "remote", "set-url", "--push", "origin", pushRemote)

	initial := runGitIntegration(t, work, "rev-parse", "HEAD")
	runGitIntegration(t, work, "push", fetchRemote, "refs/heads/main:refs/heads/main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "content.txt"), []byte("second\n"), 0o600))
	runGitIntegration(t, work, "commit", "-am", "second")
	second := runGitIntegration(t, work, "rev-parse", "HEAD")
	client := integrationClient(t)

	_, err := client.Push(t.Context(), work, "origin", pushRemote, "refs/heads/main", "refs/heads/main", false)
	require.NoError(t, err)
	assert.Equal(t, second, runGitIntegration(t, pushRemote, "rev-parse", "refs/heads/main"))
	assert.Equal(t, initial, runGitIntegration(t, fetchRemote, "rev-parse", "refs/heads/main"))

	_, err = client.Fetch(t.Context(), work, "origin", fetchRemote, "")
	require.NoError(t, err)
	assert.Equal(t, initial, runGitIntegration(t, work, "rev-parse", "refs/remotes/origin/main"))

	_, err = client.Fetch(t.Context(), work, "origin", pushRemote, "")
	require.Error(t, err)
	assert.ErrorContains(t, err, "remote_url does not match")
}

func TestIntegrationPush_RejectsMultiplePushURLsBeforeMutation(t *testing.T) {
	root := t.TempDir()
	work := initWorkingIntegrationRepo(t, root, "work")
	fetchRemote := initBareIntegrationRepo(t, root, "fetch.git")
	pushOne := initBareIntegrationRepo(t, root, "push-one.git")
	pushTwo := initBareIntegrationRepo(t, root, "push-two.git")
	runGitIntegration(t, work, "remote", "add", "origin", fetchRemote)
	runGitIntegration(t, work, "remote", "set-url", "--add", "--push", "origin", pushOne)
	runGitIntegration(t, work, "remote", "set-url", "--add", "--push", "origin", pushTwo)
	client := integrationClient(t)

	_, err := client.Push(t.Context(), work, "origin", pushOne, "refs/heads/main", "refs/heads/main", false)

	require.Error(t, err)
	assert.ErrorContains(t, err, "exactly one push URL")
	assert.Error(t, runGitIntegrationError(t, pushOne, "rev-parse", "refs/heads/main"))
	assert.Error(t, runGitIntegrationError(t, pushTwo, "rev-parse", "refs/heads/main"))
}

func TestIntegrationPull_RequiresExactFetchURL(t *testing.T) {
	root := t.TempDir()
	source := initWorkingIntegrationRepo(t, root, "source")
	remote := initBareIntegrationRepo(t, root, "remote.git")
	runGitIntegration(t, source, "push", remote, "refs/heads/main:refs/heads/main")
	runGitIntegration(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	checkout := filepath.Join(root, "checkout")
	runGitIntegration(t, root, "clone", remote, checkout)
	client := integrationClient(t)

	_, err := client.Pull(t.Context(), checkout, "origin", remote+"-mismatch", "main", false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "remote_url does not match")

	out, err := client.Pull(context.Background(), checkout, "origin", remote, "main", false)
	require.NoError(t, err)
	assert.Contains(t, out, "Already up to date")
}

func TestIntegrationPush_RejectsUnknownRemoteBeforeMutation(t *testing.T) {
	root := t.TempDir()
	work := initWorkingIntegrationRepo(t, root, "work")
	remote := initBareIntegrationRepo(t, root, "remote.git")
	client := integrationClient(t)

	_, err := client.Push(t.Context(), work, "missing", remote, "refs/heads/main", "refs/heads/main", false)

	require.Error(t, err)
	assert.ErrorContains(t, err, fmt.Sprintf("remote %q is not configured", "missing"))
	assert.Error(t, runGitIntegrationError(t, remote, "rev-parse", "refs/heads/main"))
}
