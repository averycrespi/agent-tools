package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/stretchr/testify/require"
)

func TestCLIGitResponseCoordinatesAndPolicy(t *testing.T) {
	for _, raw := range []string{"https://EXAMPLE.com/team/repo", "https://example.com:8443/team/repo.git", "https://[2001:db8::1]/repo", "https://127.0.0.1/repo"} {
		canonical, err := gitpolicy.Locator(raw)
		require.NoError(t, err)
		require.True(t, validGitLocatorResponse(canonical), canonical)
	}
	for _, raw := range []string{"https://example.com/repo", "https://EXAMPLE.com:443/repo", "https://example.com:0443/repo", "https://*.example.com:443/repo", "https://user@example.com:443/repo", "https://example.com:443/a%2Fb", "https://example.com:443/a/../b", "https://example.com:443/repo?", "https://example.com:443/repo#", "https://example.com:443/repo/", "https://example.com:443//repo"} {
		require.False(t, validGitLocatorResponse(raw), raw)
	}
	id := idForSecurityTest()
	g := contract.GitGrant{ID: id, PrincipalID: id, RepositoryID: id, Revision: "1", State: contract.GrantActive, CreatedAt: "2026-09-21T00:00:00.000000000Z", UpdatedAt: "2026-09-21T00:00:00.000000000Z"}
	policy := contract.GitPolicy{Version: 1, Read: true, Refs: []contract.GitRefRule{
		{Ref: contract.GitRefSelector{Kind: "prefix", Value: "refs/heads/"}, Actions: []string{"update", "create", "delete"}},
		{Ref: contract.GitRefSelector{Kind: "exact", Value: "refs/tags/v1"}, Actions: []string{"create"}},
	}}
	canonical, err := gitpolicy.Normalize(policy)
	require.NoError(t, err)
	g.Policy = canonical
	require.True(t, validGitGrant(g))
	for _, mutate := range []func(*contract.GitPolicy){
		func(p *contract.GitPolicy) { p.Read = false },
		func(p *contract.GitPolicy) { p.Refs = append(p.Refs, p.Refs[0]) },
		func(p *contract.GitPolicy) { p.Refs[0].Ref.Value = "refs/tags/../bad" },
		func(p *contract.GitPolicy) {
			p.Refs[0].Ref.Value = "refs/tags/" + strings.Repeat("x", contract.GitRefBytes)
		},
		func(p *contract.GitPolicy) { p.Refs[0].Actions = []string{"update", "create"} },
		func(p *contract.GitPolicy) { p.Refs[0].Actions = []string{"create", "create"} },
		func(p *contract.GitPolicy) { p.Refs[0].Actions = []string{"force"} },
		func(p *contract.GitPolicy) { p.Refs[1].Ref.Value = "refs/heads" },
	} {
		g.Policy, err = gitpolicy.Normalize(policy)
		require.NoError(t, err)
		mutate(&g.Policy)
		require.False(t, validGitGrant(g))
	}
}

func TestCLIGitRepositoryExactPreconditionsAndNoReplay(t *testing.T) {
	for _, scenario := range []string{"preflight", "explicit", "uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			id := idForSecurityTest()
			etag := gitWireETag("repository", id, "1")
			resource := contract.GitRepository{ID: id, GitRepositoryDefinition: contract.GitRepositoryDefinition{Name: "Repository", URL: "https://example.com:443/team/repo", Aliases: []string{}}, Revision: "1", AliasRevision: "1", CreatedAt: "2026-09-21T00:00:00.000000000Z", UpdatedAt: "2026-09-21T00:00:00.000000000Z"}
			input := `{"name":"Renamed","url":"https://example.com/team/repo","aliases":[],"credential_id":null}`
			var reads, writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/v2/git/repositories/"+id, r.URL.Path)
				require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				if r.Method == http.MethodGet {
					reads.Add(1)
					w.Header().Set("ETag", etag)
					require.NoError(t, json.NewEncoder(w).Encode(resource))
					return
				}
				require.Equal(t, http.MethodPatch, r.Method)
				writes.Add(1)
				require.Equal(t, etag, r.Header.Get("If-Match"))
				require.Empty(t, r.Header.Get("Idempotency-Key"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.JSONEq(t, input, string(body))
				updated := resource
				updated.Name = "Renamed"
				updated.Revision = "2"
				w.Header().Set("ETag", gitWireETag("repository", id, "2"))
				if scenario == "uncertain" {
					w.Header().Set("ETag", etag)
				}
				require.NoError(t, json.NewEncoder(w).Encode(updated))
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "repository.json")
			require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
			args := []string{"git", "repository", "update", id, "--file", path, "--yes"}
			if scenario != "preflight" {
				args = append(args, "--etag", etag)
			}
			output, err := executePrincipalRequestETagCommand(t, server.URL, args...)
			if scenario == "uncertain" {
				require.Error(t, err)
				require.Equal(t, 8, commandExitCode(err))
				require.Contains(t, string(output), "client_outcome_uncertain")
			} else {
				require.NoError(t, err, string(output))
			}
			require.Equal(t, int32(1), writes.Load())
			expected := int32(0)
			if scenario == "preflight" {
				expected = 1
			}
			require.Equal(t, expected, reads.Load())
			require.NotContains(t, string(output), testAdministratorBearer)
		})
	}
}

func TestCLIGitResponseValidation(t *testing.T) {
	g := contract.GitRepository{ID: idForSecurityTest(), GitRepositoryDefinition: contract.GitRepositoryDefinition{Name: "Repository", URL: "https://example.com:443/team/repo", Aliases: []string{}}, Revision: "1", AliasRevision: "1", CreatedAt: "2026-09-21T00:00:00.000000000Z", UpdatedAt: "2026-09-21T00:00:00.000000000Z"}
	require.True(t, validGitRepository(g))
	g.Aliases = []string{"https://evil.example:443/team/repo"}
	require.False(t, validGitRepository(g))
	profile := contract.GitRoutingProfile{Origins: []string{"https://example.com:443"}, Revision: "1"}
	require.True(t, validGitProfile(profile))
	profile.Active = true
	require.False(t, validGitProfile(profile))
}
