//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func alterDemoGitTrust(t *testing.T, home, scenario string) {
	t.Helper()
	path := filepath.Join(home, "demo-git-trust.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var trust map[string]string
	require.NoError(t, json.Unmarshal(raw, &trust))
	switch scenario {
	case "git-trust-malformed":
		raw = []byte(`{"origin":`)
	case "git-trust-permissions":
		require.NoError(t, os.Chmod(path, 0644))
		return
	case "git-trust-symlink":
		require.NoError(t, os.Rename(path, path+".original"))
		require.NoError(t, os.Symlink(path+".original", path))
		return
	case "git-trust-hardlink":
		require.NoError(t, os.Link(path, path+".link"))
		return
	case "git-trust-wrong-host":
		trust["origin"] = "https://127.0.0.2:443"
	case "git-trust-wrong-origin":
		trust["origin"] = "https://127.0.0.1:1"
	case "git-trust-untrusted":
		_, certificate, err := demoGitCertificate()
		require.NoError(t, err)
		trust["certificate"] = string(certificate)
	}
	if scenario != "git-trust-malformed" {
		raw, err = json.Marshal(trust)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(path, raw, 0600))
}

func verifyDemoGit(t *testing.T, c *client, root string, manifest readyManifest) {
	t.Helper()
	profile := c.get("git/routing-profile")
	require.Equal(t, true, value(profile, "active"))
	require.Equal(t, []any{"https://127.0.0.1:" + manifest.Fixtures["git"]}, rows(profile, "origins"))
	credentials := rows(c.get("git/credentials"), "items")
	repositories := rows(c.get("git/repositories"), "items")
	grants := rows(c.get("git/grants"), "items")
	require.Len(t, credentials, 1)
	require.Len(t, repositories, 1)
	require.Len(t, grants, 1)
	credential := credentials[0].(map[string]any)
	repository := repositories[0].(map[string]any)
	grant := grants[0].(map[string]any)
	require.Equal(t, "Demo Notes", text(repository, "name"))
	require.Equal(t, text(credential, "id"), text(repository, "credential_id"))
	require.Equal(t, text(repository, "id"), text(grant, "repository_id"))
	require.Equal(t, true, value(credential, "available"))
	require.Equal(t, true, value(grant, "policy", "read"))
	require.Empty(t, rows(grant, "policy", "refs"))
	references := rows(credential, "referencing_repositories")
	require.Len(t, references, 1)
	require.Equal(t, text(repository, "id"), text(references[0].(map[string]any), "id"))
	principal := c.get("principals/" + text(grant, "principal_id"))
	require.Equal(t, "Demo Explorer", text(principal, "display_name"))
	history := rows(c.get("git/traffic"), "items")
	require.Len(t, history, 2)
	outcomes := map[string]bool{}
	public := []any{profile, credentials, repositories, grants, history}
	for _, item := range history {
		row := item.(map[string]any)
		detail := c.get("git/traffic/" + text(row, "admission", "id"))
		require.Equal(t, row, detail)
		public = append(public, detail)
		require.Equal(t, text(repository, "id"), text(row, "admission", "repository", "id"))
		require.Equal(t, "Demo Notes", text(row, "admission", "policy", "repository_name"))
		require.Equal(t, "read_discovery", text(row, "admission", "operation"))
		agent := c.get("principals/" + text(row, "admission", "principal", "id"))
		if value(row, "admission", "allowed") == true {
			outcomes["allowed"] = true
			require.Equal(t, "Demo Explorer", text(agent, "display_name"))
			require.Equal(t, float64(200), value(row, "completion", "status"))
			require.Equal(t, true, value(row, "completion", "transfer_complete"))
			require.Equal(t, "nonmutation", text(row, "completion", "outcome"))
			require.Equal(t, text(credential, "id"), text(row, "admission", "material", "credential", "id"))
		} else {
			outcomes["denied"] = true
			require.Equal(t, "Demo Reader", text(agent, "display_name"))
			require.Equal(t, float64(0), value(row, "admission", "policy", "grant_count"))
			require.Nil(t, value(row, "completion"))
		}
	}
	require.Equal(t, map[string]bool{"allowed": true, "denied": true}, outcomes)
	counts, err := gitFixtureCounts(t.Context(), root, manifest.Fixtures["git"])
	require.NoError(t, err)
	require.Equal(t, float64(1), value(counts, "allowed"))
	require.Equal(t, float64(0), value(counts, "rejected"))
	require.Equal(t, history, rows(c.get("git/traffic"), "items"), "no recurring Git activity")
	require.NoError(t, c.err)
	secret, err := os.ReadFile(filepath.Join(root, "git-upstream-secret"))
	require.NoError(t, err)
	raw, err := json.Marshal(public)
	require.NoError(t, err)
	require.False(t, bytes.Contains(raw, secret), "upstream secret in public Git records")
	ready, err := os.ReadFile(filepath.Join(root, "ready.json"))
	require.NoError(t, err)
	require.False(t, bytes.Contains(ready, secret), "upstream secret in readiness manifest")
}
