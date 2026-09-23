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

func TestCLIHTTPGrantExactPreconditionsAndUncertainAcknowledgment(t *testing.T) {
	for _, scenario := range []string{"preflight", "explicit", "uncertain", "identity"} {
		t.Run(scenario, func(t *testing.T) {
			id := idForSecurityTest()
			etag := `"http-grant-` + id + `-1"`
			resource := contract.HTTPGrant{ID: id, PrincipalID: id, Revision: "1", State: contract.GrantActive, Policy: json.RawMessage(`{"version":1,"type":"block_destination","destination":{"host":"example.com","port":443}}`), CreatedAt: "2026-09-21T00:00:00.000000000Z", UpdatedAt: "2026-09-21T00:00:00.000000000Z"}
			var reads, writes atomic.Int32
			input := `{"principal_id":"` + id + `","description":null,"expires_at":null,"policy":` + string(resource.Policy) + `}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/v2/http/grants/"+id, r.URL.Path)
				require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				switch r.Method {
				case http.MethodGet:
					reads.Add(1)
					w.Header().Set("ETag", etag)
					require.NoError(t, json.NewEncoder(w).Encode(resource))
				case http.MethodPatch:
					writes.Add(1)
					require.Equal(t, etag, r.Header.Get("If-Match"))
					require.Empty(t, r.Header.Get("Idempotency-Key"))
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.JSONEq(t, input, string(body))
					updated := resource
					updated.Revision = "2"
					w.Header().Set("ETag", `"http-grant-`+id+`-2"`)
					if scenario == "uncertain" {
						w.Header().Set("ETag", etag)
					}
					if scenario == "identity" {
						updated.PrincipalID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
					}
					require.NoError(t, json.NewEncoder(w).Encode(updated))
				default:
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "grant.json")
			require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
			args := []string{"http", "grant", "update", id, "--file", path, "--yes"}
			if scenario != "preflight" {
				args = append(args, "--etag", etag)
			}
			output, err := executePrincipalRequestETagCommand(t, server.URL, args...)
			if scenario == "uncertain" || scenario == "identity" {
				require.Error(t, err)
				require.Equal(t, 8, commandExitCode(err))
				require.Contains(t, string(output), "client_outcome_uncertain")
			} else {
				require.NoError(t, err, string(output))
			}
			require.Equal(t, int32(1), writes.Load())
			expectedReads := int32(0)
			if scenario == "preflight" {
				expectedReads = 1
			}
			require.Equal(t, expectedReads, reads.Load())
			require.NotContains(t, string(output), testAdministratorBearer)
		})
	}
}

func TestCLIHTTPPreviewDoesNotRequireMutationConfirmation(t *testing.T) {
	id := idForSecurityTest()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v2/http/access-preview", r.URL.Path)
		require.Empty(t, r.Header.Get("If-Match"))
		require.Empty(t, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		require.NoError(t, json.NewEncoder(w).Encode(contract.HTTPAccessPreview{Default: contract.HTTPDefaultBlock, PolicyOnly: true, Decision: contract.HTTPDecision{Version: 1, Principal: contract.HTTPRevisionRef{ID: id, Revision: 1}, PolicyRevision: 1, DefaultRevision: 1, Transport: contract.HTTPTransportRequest, Reason: contract.HTTPReasonDefault}}))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "preview.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"principal_id":"`+id+`","url":"https://example.com/private?preview-canary=1","method":"GET"}`), 0o600))
	output, err := executePrincipalRequestETagCommand(t, server.URL, "http", "test-access", "--file", path)
	require.NoError(t, err, string(output))
	require.Equal(t, int32(1), calls.Load())
	require.NotContains(t, string(output), "preview-canary")
	require.Contains(t, string(output), `"policy_only":true`)
	require.Contains(t, string(output), `"network_verified":false`)
}
