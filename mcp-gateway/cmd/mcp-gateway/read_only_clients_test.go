package main

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIReadOnlyGrantCreationAndReadback(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, mode := range []string{"direct", "file", "false", "lost response restriction"} {
		t.Run(mode, func(t *testing.T) {
			calls := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				calls <- body
				var input contract.Grant
				if json.Unmarshal(body, &input) != nil {
					w.WriteHeader(400)
					return
				}
				var grant contract.Grant
				_ = json.Unmarshal([]byte(grantETagBody(id, "1", "Read access")), &grant)
				grant.ReadOnly = input.ReadOnly && mode != "lost response restriction"
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(grant)
			}))
			defer server.Close()
			args := []string{"grant", "create", "--principal-id", id, "--server-id", id, "--effect", "allow", "--read-only"}
			if mode == "file" {
				file := filepath.Join(t.TempDir(), "grant.json")
				require.NoError(t, os.WriteFile(file, []byte(`{"description":null,"principal_id":"`+id+`","server_id":"`+id+`","effect":"allow","upstream_name":null,"constraint":null,"expires_at":null,"read_only":true}`), 0600))
				args = []string{"grant", "create", "--file", file}
			}
			if mode == "false" {
				args[len(args)-1] = "--read-only=false"
			}
			output, err := executePrincipalRequestETagCommand(t, server.URL, args...)
			require.Len(t, calls, 1)
			var submitted contract.Grant
			require.NoError(t, json.Unmarshal(<-calls, &submitted))
			assert.Equal(t, mode != "false", submitted.ReadOnly)
			if mode == "lost response restriction" {
				require.Error(t, err)
				assert.Equal(t, 8, commandExitCode(err))
				assert.Contains(t, string(output), "Nothing was replayed")
				return
			}
			require.NoError(t, err, "%s", output)
			var grant contract.Grant
			require.NoError(t, json.Unmarshal(output, &grant))
			assert.Equal(t, submitted.ReadOnly, grant.ReadOnly)
			table := grantTable([]contract.Grant{grant}, false)
			if grant.ReadOnly {
				assert.Contains(t, strings.Join(table.Rows[0], " "), "read-only tools")
			} else {
				assert.Contains(t, strings.Join(table.Rows[0], " "), "unrestricted")
			}
		})
	}
}

func TestCLIReadOnlyInputValidationBeforeHTTP(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	grant := []string{"grant", "create", "--principal-id", id, "--server-id", id, "--effect", "allow", "--read-only"}
	approval := []string{"grant-request", "approve", id, "--scope", "tool", "--target", "demo.read", "--read-only", "--yes"}
	for _, args := range [][]string{
		append(append([]string{}, grant...), "--effect", "deny"),
		append(append([]string{}, grant...), "--upstream-name", "read"),
		approval,
		append(append([]string{}, approval...), "--scope", "server"),
	} {
		output, err := executePrincipalRequestETagCommand(t, "http://127.0.0.1:1", args...)
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err), "%s", output)
		assert.Contains(t, string(output), "read_only=true requires server")
	}
	for _, selector := range []string{"null", `"true"`, "1", "{}"} {
		file := filepath.Join(t.TempDir(), "grant.json")
		require.NoError(t, os.WriteFile(file, []byte(`{"description":null,"principal_id":"`+id+`","server_id":"`+id+`","effect":"allow","upstream_name":null,"constraint":null,"expires_at":null,"read_only":`+selector+`}`), 0600))
		output, err := executePrincipalRequestETagCommand(t, "http://127.0.0.1:1", "grant", "create", "--file", file)
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err), "%s", output)
		assert.Contains(t, string(output), "read_only must be Boolean")
		output, err = executePrincipalRequestETagCommand(t, "http://127.0.0.1:1", "grant", "create", "--file", file, "--read-only=false")
		require.Error(t, err)
		assert.Contains(t, string(output), "not both")
		approvalFile := filepath.Join(t.TempDir(), "approval.json")
		require.NoError(t, os.WriteFile(approvalFile, []byte(`{"description":null,"approved_policy":{"scope":"server","target":"demo","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true,"read_only":`+selector+`}}`), 0o600))
		output, err = executePrincipalRequestETagCommand(t, "http://127.0.0.1:1", "grant-request", "approve", id, "--file", approvalFile, "--yes")
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err), "%s", output)
		assert.Contains(t, string(output), "read_only must be Boolean")
		output, err = executePrincipalRequestETagCommand(t, "http://127.0.0.1:1", "grant-request", "approve", id, "--file", approvalFile, "--read-only=false", "--yes")
		require.Error(t, err)
		assert.Contains(t, string(output), "not both")
	}
}

func TestCLIReadOnlyFileCombinationDiagnostics(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, test := range []struct {
		name, effect, upstream, constraint, scope string
		acknowledged                              bool
	}{
		{name: "deny", effect: "deny", upstream: "null", constraint: "null", scope: "tool"},
		{name: "tool", effect: "allow", upstream: `"read"`, constraint: "null", scope: "tool"},
		{name: "constraints", effect: "allow", upstream: "null", constraint: `{"equals":{"/x":1}}`, scope: "server", acknowledged: true},
		{name: "acknowledgement", effect: "allow", upstream: "null", constraint: "null", scope: "server"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name != "acknowledgement" {
				file := filepath.Join(t.TempDir(), "grant.json")
				body, err := json.Marshal(map[string]any{"description": nil, "principal_id": id, "server_id": id, "effect": test.effect, "upstream_name": json.RawMessage(test.upstream), "constraint": json.RawMessage(test.constraint), "expires_at": nil, "read_only": true})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(file, body, 0o600))
				output, err := executePrincipalRequestETagCommand(t, "http://127.0.0.1:1", "grant", "create", "--file", file)
				require.Error(t, err)
				assert.Equal(t, 2, commandExitCode(err))
				assert.Contains(t, string(output), "read_only=true requires server ALLOW scope with no upstream tool or argument constraints")
			}
			file := filepath.Join(t.TempDir(), "approval.json")
			body, err := json.Marshal(map[string]any{"description": nil, "approved_policy": map[string]any{"scope": test.scope, "target": "demo", "constraint": json.RawMessage(test.constraint), "duration_seconds": nil, "future_tools_acknowledged": test.acknowledged, "read_only": true}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(file, body, 0o600))
			output, err := executePrincipalRequestETagCommand(t, "http://127.0.0.1:1", "grant-request", "approve", id, "--file", file, "--yes")
			require.Error(t, err)
			assert.Equal(t, 2, commandExitCode(err))
			assert.Contains(t, string(output), "read_only=true requires server scope, null constraints, and future-tool acknowledgement")
		})
	}
}

func TestCLIReadOnlyApprovalPreservationAndNoReplay(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, test := range []struct {
		name              string
		requestedReadOnly bool
		file              bool
		scope             string
		readOnly          bool
		etag              string
		status            int
		wantExit          int
		wantMutations     int
	}{
		{name: "preserve", requestedReadOnly: true, scope: "server", readOnly: true, status: 200, wantMutations: 1},
		{name: "preserve file", file: true, requestedReadOnly: true, scope: "server", readOnly: true, status: 200, wantMutations: 1},
		{name: "file cannot remove restriction", file: true, requestedReadOnly: true, scope: "server", wantExit: 2},
		{name: "narrow", scope: "server", readOnly: true, etag: contract.GrantRequestETag(id, "1"), status: 200, wantMutations: 1},
		{name: "remove restriction", requestedReadOnly: true, scope: "server", wantExit: 2},
		{name: "server to tool", requestedReadOnly: true, scope: "tool", wantExit: 2},
		{name: "explicit stale", requestedReadOnly: true, scope: "server", readOnly: true, etag: contract.GrantRequestETag(id, "2"), wantExit: 2},
		{name: "racing stale", requestedReadOnly: true, scope: "server", readOnly: true, status: 412, wantExit: 5, wantMutations: 1},
		{name: "uncertain", requestedReadOnly: true, scope: "server", readOnly: true, status: 503, wantExit: 8, wantMutations: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := make(chan string, 3)
			requested := contract.Policy{Scope: contract.PolicyServer, Target: "demo", FutureToolsAcknowledged: true, ReadOnly: test.requestedReadOnly}
			responseBody := func(state, revision string, approved json.RawMessage) []byte {
				var item map[string]any
				require.NoError(t, json.Unmarshal([]byte(grantRequestETagBody(id, state, revision, approved, nil)), &item))
				item["requested_policy"] = requested
				encoded, err := json.Marshal(item)
				require.NoError(t, err)
				return encoded
			}
			pending := responseBody("pending", "1", nil)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls <- r.Method
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				if r.Method == http.MethodGet {
					w.Header().Set("ETag", contract.GrantRequestETag(id, "1"))
					_, _ = w.Write(pending)
					return
				}
				if test.status != 200 {
					code := "stale_revision"
					if test.status == 503 {
						code = "storage_unavailable"
					}
					w.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
					w.WriteHeader(test.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"status": test.status, "code": code, "title": "Approval failed."})
					return
				}
				var input contract.GrantRequestApproval
				_ = json.NewDecoder(r.Body).Decode(&input)
				if !input.ApprovedPolicy.ReadOnly {
					w.WriteHeader(400)
					return
				}
				approved, _ := json.Marshal(input.ApprovedPolicy)
				w.Header().Set("ETag", contract.GrantRequestETag(id, "2"))
				_, _ = w.Write(responseBody("approved", "2", approved))
			}))
			defer server.Close()
			args := []string{"grant-request", "approve", id, "--scope", test.scope, "--target", "demo", "--yes"}
			if test.scope == "server" {
				args = append(args, "--acknowledge-future-tools")
			}
			if test.readOnly {
				args = append(args, "--read-only")
			}
			if test.file {
				path := filepath.Join(t.TempDir(), "approval.json")
				body, marshalErr := json.Marshal(contract.GrantRequestApproval{ApprovedPolicy: contract.Policy{Scope: contract.PolicyScope(test.scope), Target: "demo", FutureToolsAcknowledged: test.scope == "server", ReadOnly: test.readOnly}})
				require.NoError(t, marshalErr)
				require.NoError(t, os.WriteFile(path, body, 0o600))
				args = []string{"grant-request", "approve", id, "--file", path, "--yes"}
			}
			if test.etag != "" {
				args = append(args, "--etag", test.etag)
			}
			output, err := executePrincipalRequestETagCommand(t, server.URL, args...)
			if test.wantExit == 0 {
				require.NoError(t, err, "%s", output)
				var approved contract.GrantRequest
				require.NoError(t, json.Unmarshal(output, &approved))
				require.True(t, approved.ApprovedPolicy.ReadOnly)
				table := grantRequestTable([]contract.GrantRequestSummary{approved.GrantRequestSummary}, nil)
				assert.Equal(t, policyAccessSummary(requested), table.Rows[0][11])
				assert.Equal(t, "read-only server tools", table.Rows[0][12])
			} else {
				require.Error(t, err)
				assert.Equal(t, test.wantExit, commandExitCode(err), "%s", output)
			}
			require.Len(t, calls, 1+test.wantMutations)
			assert.Equal(t, http.MethodGet, <-calls)
			if test.wantMutations == 1 {
				assert.Equal(t, http.MethodPost, <-calls)
			}
		})
	}
}

func TestCLIReadOnlyHelp(t *testing.T) {
	for _, path := range [][]string{{"grant", "create"}, {"grant-request", "approve"}} {
		cmd, _, err := newRootCmd().Find(path)
		require.NoError(t, err)
		for _, phrase := range []string{"readOnlyHint=true", "future tools", "trusted server declarations", "not side-effect isolation", "Other ALLOW", "DENY still wins"} {
			assert.Contains(t, cmd.Long, phrase)
		}
	}
}
