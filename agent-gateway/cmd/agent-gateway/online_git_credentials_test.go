package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func gitCLIResource() contract.GitCredential {
	return contract.GitCredential{ID: idForSecurityTest(), GitCredentialDefinition: contract.GitCredentialDefinition{Name: "Git access", Origin: "https://example.com:443", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, Revision: "7", Available: true, References: []contract.GitCredentialReference{}, CreatedAt: "2026-09-21T00:00:00.000000000Z", UpdatedAt: "2026-09-21T00:00:00.000000000Z"}
}
func TestCLIGitCredentialDeleteShapeAndLatchedOutcome(t *testing.T) {
	for _, code := range []string{"", "storage_unavailable", "keyring_unavailable"} {
		t.Run("delete_"+code, func(t *testing.T) {
			c := gitCLIResource()
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				require.Equal(t, http.MethodDelete, r.Method)
				require.Equal(t, "/api/v2/git/credentials/"+c.ID, r.URL.Path)
				require.Equal(t, contract.MediaTypeJSON, r.Header.Get("Content-Type"))
				require.Equal(t, gitWireETag("credential", c.ID, c.Revision), r.Header.Get("If-Match"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, `{}`, string(body))
				if code == "" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
				w.WriteHeader(http.StatusServiceUnavailable)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"status": 503, "code": code, "title": "Unavailable"}))
			}))
			defer server.Close()
			output, err := executePrincipalRequestETagCommand(t, server.URL, "git", "credential", "delete", c.ID, "--yes", "--etag", gitWireETag("credential", c.ID, c.Revision))
			if code == "" {
				require.NoError(t, err, string(output))
			} else {
				require.Error(t, err)
				if code == "storage_unavailable" {
					require.Equal(t, 8, commandExitCode(err))
					require.Contains(t, string(output), "client_outcome_uncertain")
				} else {
					require.Contains(t, string(output), "keyring_unavailable")
				}
			}
			require.Equal(t, int32(1), writes.Load())
		})
	}
}

func TestCLIGitCredentialPreconditionsPrivacyAndNoReplay(t *testing.T) {
	for _, scenario := range []string{"preflight", "explicit", "uncertain", "secret_response"} {
		t.Run(scenario, func(t *testing.T) {
			resource := gitCLIResource()
			etag := gitWireETag("credential", resource.ID, resource.Revision)
			var reads, writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				if r.Method == http.MethodGet {
					reads.Add(1)
					require.Equal(t, "/api/v2/git/credentials/"+resource.ID, r.URL.Path)
					w.Header().Set("ETag", etag)
					require.NoError(t, json.NewEncoder(w).Encode(resource))
					return
				}
				writes.Add(1)
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/api/v2/git/credentials/"+resource.ID+"/rotate", r.URL.Path)
				require.Equal(t, etag, r.Header.Get("If-Match"))
				require.Empty(t, r.Header.Get("Idempotency-Key"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.JSONEq(t, `{"secret":"private-git-cli-canary"}`, string(body))
				resource.Revision = "8"
				w.Header().Set("ETag", gitWireETag("credential", resource.ID, resource.Revision))
				if scenario == "uncertain" {
					w.Header().Set("ETag", etag)
				}
				if scenario == "secret_response" {
					raw, err := json.Marshal(resource)
					require.NoError(t, err)
					var fields map[string]any
					require.NoError(t, json.Unmarshal(raw, &fields))
					fields["secret"] = "private-git-cli-canary"
					require.NoError(t, json.NewEncoder(w).Encode(fields))
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(resource))
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "secret.json")
			require.NoError(t, os.WriteFile(path, []byte(`{"secret":"private-git-cli-canary"}`), 0o600))
			args := []string{"git", "credential", "rotate", resource.ID, "--file", path, "--yes"}
			if scenario != "preflight" {
				args = append(args, "--etag", etag)
			}
			output, err := executePrincipalRequestETagCommand(t, server.URL, args...)
			if scenario == "uncertain" || scenario == "secret_response" {
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
			require.NotContains(t, string(output), "private-git-cli-canary")
			require.NotContains(t, string(output), testAdministratorBearer)
		})
	}
}
func TestCLIGitCredentialMalformedIngressRejectsBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, input := range []string{`{"secret":"private","fallback":true}`, `{"secret":"private","secret":"other"}`, `{"secret":`} {
		path := filepath.Join(t.TempDir(), "invalid.json")
		require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
		c := gitCLIResource()
		output, err := executePrincipalRequestETagCommand(t, server.URL, "git", "credential", "rotate", c.ID, "--file", path, "--yes", "--etag", gitWireETag("credential", c.ID, c.Revision))
		require.Error(t, err)
		require.NotContains(t, string(output), input)
	}
	require.Zero(t, requests.Load())
}
func TestCLIGitCredentialClosedResponseBoundaries(t *testing.T) {
	c := gitCLIResource()
	require.True(t, validGitCredential(c))
	c.Origin = "https://example.com"
	require.False(t, validGitCredential(c))
	c = gitCLIResource()
	c.References = nil
	require.False(t, validGitCredential(c))
	c = gitCLIResource()
	c.Recipe.Header = "Connection"
	require.False(t, validGitCredential(c))
}
