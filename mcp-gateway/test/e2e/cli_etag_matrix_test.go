//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliETagMatrixRequest struct {
	method string
	path   string
	etag   string
}

func TestCLIETagMatrix(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	credentialInput := filepath.Join(t.TempDir(), "credential.json")
	require.NoError(t, os.WriteFile(credentialInput, []byte(`{"kind":"static_credential","expected_revision":"0","values":{"token":"secret-canary"}}`), 0o600))
	approveInput := filepath.Join(t.TempDir(), "approve.json")
	require.NoError(t, os.WriteFile(approveInput, []byte(`{"description":"Approved access","approved_policy":{"scope":"tool","target":"example_tool","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}}`), 0o600))

	cases := []struct {
		name             string
		resource         string
		args             func(*testing.T) []string
		explicitStillGET bool
		occupied         bool
	}{
		{name: "server update", resource: "server", args: func(*testing.T) []string { return []string{"server", "update", id, "--display-name", "Renamed"} }},
		{name: "server delete", resource: "server", args: func(*testing.T) []string { return []string{"server", "delete", id, "--yes"} }},
		{name: "server operation start", resource: "server", args: func(*testing.T) []string {
			return []string{"server", "operation", "start", id, "--kind", "retry", "--idempotency-key", "matrix-operation"}
		}},
		{name: "server credential replace", resource: "server", args: func(*testing.T) []string {
			return []string{"server", "credential", "replace", id, "--file", credentialInput, "--yes"}
		}},
		{name: "principal update", resource: "principal", args: func(*testing.T) []string { return []string{"principal", "update", id, "--display-name", "Renamed"} }},
		{name: "principal issue", resource: "principal", explicitStillGET: true, args: func(t *testing.T) []string {
			return []string{"principal", "credential", "issue", id, "--secret-output", filepath.Join(t.TempDir(), "issued"), "--yes"}
		}},
		{name: "principal rotate", resource: "principal", explicitStillGET: true, occupied: true, args: func(t *testing.T) []string {
			return []string{"principal", "credential", "rotate", id, "--secret-output", filepath.Join(t.TempDir(), "rotated"), "--yes"}
		}},
		{name: "principal revoke", resource: "principal", occupied: true, args: func(*testing.T) []string { return []string{"principal", "credential", "revoke", id, "--yes"} }},
		{name: "grant request approve", resource: "grant-request", args: func(*testing.T) []string {
			return []string{"grant-request", "approve", id, "--file", approveInput, "--yes"}
		}},
		{name: "grant request reject", resource: "grant-request", args: func(*testing.T) []string {
			return []string{"grant-request", "reject", id, "--reason", "not_approved", "--yes"}
		}},
	}

	for _, test := range cases {
		for _, explicit := range []bool{false, true} {
			mode := "omitted"
			if explicit {
				mode = "explicit"
			}
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				server, requests := newCLIETagMatrixServer(t, id, test.occupied)
				args := test.args(t)
				expectedETag := cliETagFor(test.resource, id, "7")
				if explicit {
					args = append(args, "--etag", expectedETag)
				}
				result := runETagMatrixBinary(t, server.URL, args...)
				require.Equal(t, 0, result.ExitCode, "%s", result.Stderr)
				assert.NotContains(t, string(result.Stdout), "secret-canary")
				assert.NotContains(t, string(result.Stderr), "secret-canary")
				expectedCount := 2
				if explicit && !test.explicitStillGET {
					expectedCount = 1
				}
				require.Len(t, requests, expectedCount)
				if expectedCount == 2 {
					assert.Equal(t, http.MethodGet, (<-requests).method)
				}
				mutation := <-requests
				assert.NotEqual(t, http.MethodGet, mutation.method)
				assert.Equal(t, expectedETag, mutation.etag)
			})
		}
	}

	t.Run("server auth flow start", func(t *testing.T) {
		for _, explicit := range []bool{false, true} {
			authorization := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
			requests := make(chan cliETagMatrixRequest, 3)
			server := httptest.NewServer(cliETagMatrixHandler(id, false, requests, authorization.URL+"/authorize"))
			bearerPath := writeETagMatrixBearer(t)
			harness := newGatewayHarness(t)
			etag := ""
			if explicit {
				etag = contract.ServerETag(id, "7")
			}
			result := runAuthFlowStartPTYWithETag(t, harness, bearerPath, server.URL, authorization.URL+"/authorize", id, etag)
			require.Equal(t, 0, result.ExitCode, "%s", result.Stderr)
			expectedCount := 2
			if explicit {
				expectedCount = 1
			}
			require.Len(t, requests, expectedCount)
			if expectedCount == 2 {
				assert.Equal(t, http.MethodGet, (<-requests).method)
			}
			assert.Equal(t, contract.ServerETag(id, "7"), (<-requests).etag)
			server.Close()
			authorization.Close()
		}
	})
}

func runETagMatrixBinary(t *testing.T, address string, args ...string) testutil.ProcessResult {
	t.Helper()
	harness := newGatewayHarness(t)
	return runCLIAt(t, harness, writeETagMatrixBearer(t), address, append(args, "--output", "json")...)
}

func writeETagMatrixBearer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(path, []byte("mgw_admin_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o600))
	return path
}

func cliETagFor(resource, id, revision string) string {
	if resource == "server" {
		return contract.ServerETag(id, revision)
	}
	if resource == "principal" {
		return contract.PrincipalETag(id, revision)
	}
	return contract.GrantRequestETag(id, "1")
}

func newCLIETagMatrixServer(t *testing.T, id string, occupied bool) (*httptest.Server, chan cliETagMatrixRequest) {
	t.Helper()
	requests := make(chan cliETagMatrixRequest, 3)
	server := httptest.NewServer(cliETagMatrixHandler(id, occupied, requests, "https://example.test/authorize"))
	t.Cleanup(server.Close)
	return server, requests
}

func cliETagMatrixHandler(id string, occupied bool, requests chan cliETagMatrixRequest, authorizationURL string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		requests <- cliETagMatrixRequest{method: request.Method, path: request.URL.Path, etag: request.Header.Get("If-Match")}
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		if request.Method == http.MethodGet {
			switch {
			case strings.HasPrefix(request.URL.Path, "/api/v1/servers/"):
				response.Header().Set("ETag", contract.ServerETag(id, "7"))
				_, _ = response.Write([]byte(`{"id":"` + id + `","desired_revision":"7"}`))
			case strings.HasPrefix(request.URL.Path, "/api/v1/principals/"):
				response.Header().Set("ETag", contract.PrincipalETag(id, "7"))
				_, _ = response.Write([]byte(cliETagPrincipal(id, "7", occupied)))
			default:
				response.Header().Set("ETag", contract.GrantRequestETag(id, "1"))
				_, _ = response.Write([]byte(cliETagGrantRequest(id, "pending", "1", nil, nil)))
			}
			return
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/operations"):
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{"operation":{"id":"` + id + `","server_id":"` + id + `","kind":"retry","target_desired_revision":"7","target_credential_revisions":{},"state":"scheduled","reason":null,"created_at":"2026-08-30T00:00:00Z","started_at":null,"finished_at":null}}`))
		case strings.HasSuffix(request.URL.Path, "/credential-replacements"):
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{"server_id":"` + id + `","kind":"static_credential","credential_revision":"1","operation":{"id":"` + id + `","server_id":"` + id + `","kind":"credential_replace","target_desired_revision":"7","target_credential_revisions":{},"state":"scheduled","reason":null,"created_at":"2026-08-30T00:00:00Z","started_at":null,"finished_at":null}}`))
		case strings.HasSuffix(request.URL.Path, "/auth-flows"):
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"flow":{"id":"` + id + `","server_id":"` + id + `","flow_state":"awaiting_callback","target_desired_revision":"7","registration_revision":"1","created_at":"2026-08-30T00:00:00Z","expires_at":"2026-08-30T01:00:00Z","finished_at":null,"reason":null},"authorization_url":"` + authorizationURL + `"}`))
		case strings.Contains(request.URL.Path, "/principals/") && strings.HasSuffix(request.URL.Path, "/credential") && request.Method == http.MethodPost:
			response.Header().Set("ETag", contract.PrincipalETag(id, "8"))
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"principal":` + cliETagPrincipal(id, "8", true) + `,"bearer":"mgw_agent_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`))
		case strings.Contains(request.URL.Path, "/principals/"):
			response.Header().Set("ETag", contract.PrincipalETag(id, "8"))
			_, _ = response.Write([]byte(cliETagPrincipal(id, "8", request.Method != http.MethodDelete)))
		case strings.HasSuffix(request.URL.Path, "/approve"):
			response.Header().Set("ETag", contract.GrantRequestETag(id, "2"))
			_, _ = response.Write([]byte(cliETagGrantRequest(id, "approved", "2", json.RawMessage(`{"scope":"tool","target":"example_tool","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}`), nil)))
		case strings.HasSuffix(request.URL.Path, "/reject"):
			response.Header().Set("ETag", contract.GrantRequestETag(id, "2"))
			_, _ = response.Write([]byte(cliETagGrantRequest(id, "rejected", "2", nil, json.RawMessage(`"not_approved"`))))
		default:
			response.Header().Set("ETag", contract.ServerETag(id, "8"))
			body := `{"server":{"id":"` + id + `","desired_revision":"8"},"operation":null}`
			if request.Method == http.MethodDelete {
				body = `{"server":{"id":"` + id + `","desired_state":"deleted","desired_revision":"8","transport":null},"operation":{"id":"` + id + `","server_id":"` + id + `","kind":"delete","target_desired_revision":"7","target_credential_revisions":{},"state":"scheduled","reason":null,"created_at":"2026-08-30T00:00:00Z","started_at":null,"finished_at":null}}`
			}
			_, _ = response.Write([]byte(body))
		}
	})
}

func cliETagPrincipal(id, revision string, occupied bool) string {
	credential := "null"
	if occupied {
		credential = `{"id":"` + id + `","fingerprint":"sha256:test","revision":"1","created_at":"2026-08-30T00:00:00Z"}`
	}
	return `{"id":"` + id + `","display_name":"Agent","state":"active","visibility":"requestable","revision":"` + revision + `","credential_revision":"1","credential":` + credential + `,"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z"}`
}

func cliETagGrantRequest(id, state, revision string, approvedPolicy, rejectionReason json.RawMessage) string {
	approved, grantID, reason := "null", "null", "null"
	if approvedPolicy != nil {
		approved, grantID = string(approvedPolicy), `"`+id+`"`
	}
	if rejectionReason != nil {
		reason = string(rejectionReason)
	}
	return `{"id":"` + id + `","principal_id":"` + id + `","state":"` + state + `","revision":"` + revision + `","requested_policy":{"scope":"tool","target":"example_tool","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false},"approved_policy":` + approved + `,"approved_grant_id":` + grantID + `,"rejection_reason":` + reason + `,"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z","closed_at":null,"resolved_server_id":"` + id + `","resolved_upstream_name":"example_tool","submitted_evidence":null,"approved_evidence":null,"current_target":{"scope":"tool","target_state":"available","active_state":null,"durable_state":null,"catalog_revision":null,"fingerprint":null,"descriptor":null}}`
}
