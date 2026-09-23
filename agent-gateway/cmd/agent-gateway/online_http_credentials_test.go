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
	"github.com/stretchr/testify/require"
)

func httpCredentialCLIResource() contract.HTTPCredential {
	return contract.HTTPCredential{ID: idForSecurityTest(), HTTPCredentialDefinition: contract.HTTPCredentialDefinition{Name: "API credential", Boundary: contract.HTTPCredentialBoundary{Host: "api.example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, Revision: "7", Available: true, References: []contract.HTTPCredentialReference{}, CreatedAt: "2026-09-21T00:00:00Z", UpdatedAt: "2026-09-21T00:00:00Z"}
}

func TestCLIHTTPCredentialPreconditionsAndPrivacy(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{true: "explicit", false: "preflight"}[explicit], func(t *testing.T) {
			var reads, rotations atomic.Int32
			resource := httpCredentialCLIResource()
			etag := contract.HTTPCredentialETag(resource.ID, resource.Revision)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				switch r.Method {
				case http.MethodGet:
					reads.Add(1)
					require.Equal(t, "/api/v2/http/credentials/"+resource.ID, r.URL.Path)
					w.Header().Set("ETag", etag)
					require.NoError(t, json.NewEncoder(w).Encode(resource))
				case http.MethodPost:
					rotations.Add(1)
					require.Equal(t, "/api/v2/http/credentials/"+resource.ID+"/rotate", r.URL.Path)
					require.Equal(t, etag, r.Header.Get("If-Match"))
					require.Empty(t, r.Header.Get("Idempotency-Key"))
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.JSONEq(t, `{"secret":"cli-private-rotation-canary"}`, string(body))
					updated := resource
					updated.Revision = "8"
					w.Header().Set("ETag", contract.HTTPCredentialETag(resource.ID, "8"))
					require.NoError(t, json.NewEncoder(w).Encode(updated))
				default:
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "secret.json")
			require.NoError(t, os.WriteFile(path, []byte(`{"secret":"cli-private-rotation-canary"}`), 0o600))
			args := []string{"http", "credential", "rotate", resource.ID, "--file", path, "--yes"}
			if explicit {
				args = append(args, "--etag", etag)
			}
			output, err := executePrincipalRequestETagCommand(t, server.URL, args...)
			require.NoError(t, err, string(output))
			require.NotContains(t, string(output), "canary")
			require.NotContains(t, string(output), testAdministratorBearer)
			require.Equal(t, int32(1), rotations.Load())
			expectedReads := int32(1)
			if explicit {
				expectedReads = 0
			}
			require.Equal(t, expectedReads, reads.Load())
		})
	}
}

func TestCLIHTTPCredentialUncertaintyHasNoReplay(t *testing.T) {
	resource := httpCredentialCLIResource()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		problem, ok := contract.ProblemForCode(contract.ProblemKeyringUnavailable)
		require.True(t, ok)
		w.WriteHeader(problem.Status)
		require.NoError(t, json.NewEncoder(w).Encode(problem))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"secret":"uncertain-private-canary"}`), 0o600))
	output, err := executePrincipalRequestETagCommand(t, server.URL, "http", "credential", "rotate", resource.ID, "--file", path, "--yes", "--etag", contract.HTTPCredentialETag(resource.ID, resource.Revision))
	require.Error(t, err)
	require.Equal(t, 8, commandExitCode(err))
	require.Equal(t, int32(1), calls.Load())
	require.NotContains(t, string(output), "canary")
	require.Contains(t, string(output), "client_outcome_uncertain")
}

func TestCLIHTTPCredentialIngressRejectsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	resource := httpCredentialCLIResource()
	for _, body := range []string{`{"secret":"canary","unknown":true}`, `{"secret":"canary","secret":"duplicate"}`, `{"secret":"` + strings.Repeat("x", 1<<20) + `"}`} {
		path := filepath.Join(t.TempDir(), "secret.json")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		output, err := executePrincipalRequestETagCommand(t, server.URL, "http", "credential", "rotate", resource.ID, "--file", path, "--yes", "--etag", contract.HTTPCredentialETag(resource.ID, resource.Revision))
		require.Error(t, err)
		require.Equal(t, 2, commandExitCode(err))
		require.NotContains(t, string(output), "canary")
	}
	require.Zero(t, calls.Load())
}
