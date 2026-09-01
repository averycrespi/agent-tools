package main

import (
	"bytes"
	"context"
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

type principalRequestETagRecord struct {
	method string
	path   string
	etag   string
}

func TestCLIPrincipalAndGrantRequestETagModes(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	cases := []struct {
		name string
		args []string
		etag string
	}{
		{name: "principal update", args: []string{"principal", "update", id, "--display-name", "Renamed"}, etag: contract.PrincipalETag(id, "7")},
		{name: "principal credential revoke", args: []string{"principal", "credential", "revoke", id, "--yes"}, etag: contract.PrincipalETag(id, "7")},
		{name: "grant update", args: []string{"grant", "update", id, "--description", "Updated access"}, etag: contract.GrantETag(id, "1")},
		{name: "grant request approve", args: []string{"grant-request", "approve", id, "--description", "Approved access", "--scope", "tool", "--target", "example_tool", "--yes"}, etag: contract.GrantRequestETag(id, "1")},
		{name: "grant request reject", args: []string{"grant-request", "reject", id, "--reason", "not_approved", "--yes"}, etag: contract.GrantRequestETag(id, "1")},
	}
	for _, test := range cases {
		t.Run(test.name+"/omitted", func(t *testing.T) {
			server, requests := newPrincipalRequestETagServer(t, id)
			output, err := executePrincipalRequestETagCommand(t, server.URL, test.args...)
			require.NoError(t, err, "%s", output)
			require.Len(t, requests, 2)
			assert.Equal(t, http.MethodGet, (<-requests).method)
			assert.Equal(t, test.etag, (<-requests).etag)
		})
		t.Run(test.name+"/explicit", func(t *testing.T) {
			server, requests := newPrincipalRequestETagServer(t, id)
			output, err := executePrincipalRequestETagCommand(t, server.URL, append(test.args, "--etag", test.etag)...)
			require.NoError(t, err, "%s", output)
			require.Len(t, requests, 1)
			assert.Equal(t, test.etag, (<-requests).etag)
		})
	}

	for _, invalid := range []struct {
		name string
		args []string
		etag string
	}{
		{name: "wrong principal", args: cases[0].args, etag: contract.PrincipalETag("01BX5ZZKBKACTAV9WEVGEMMVRZ", "7")},
		{name: "wrong grant", args: cases[2].args, etag: contract.GrantETag("01BX5ZZKBKACTAV9WEVGEMMVRZ", "1")},
		{name: "malformed grant request", args: cases[3].args, etag: `W/"grant-request-` + id + `-1"`},
	} {
		t.Run(invalid.name+" explicit rejects before HTTP", func(t *testing.T) {
			server, requests := newPrincipalRequestETagServer(t, id)
			output, err := executePrincipalRequestETagCommand(t, server.URL, append(invalid.args, "--etag", invalid.etag)...)
			require.Error(t, err)
			assert.Equal(t, 2, commandExitCode(err), "%s", output)
			assert.Len(t, requests, 0)
		})
	}
}

func TestCLIRetainedFileSecurity(t *testing.T) {
	root := newRootCmd()
	fileOwners := map[string]bool{
		"server create":             true,
		"server update":             true,
		"server credential replace": true,
		"grant create":              true,
		"grant-request approve":     true,
	}
	for _, spec := range onlineCommandSpecs() {
		path := strings.Join(spec.Path, " ")
		command, _, err := root.Find(spec.Path)
		require.NoError(t, err, path)
		assert.Equal(t, fileOwners[path], command.Flags().Lookup("file") != nil, path)
	}

	for _, test := range []struct {
		args []string
	}{
		{args: []string{"server", "create", "--transport", "secret-canary"}},
		{args: []string{"server", "credential", "replace", idForSecurityTest(), "--values", "secret-canary"}},
		{args: []string{"server", "credential", "replace", idForSecurityTest(), "--client-secret", "secret-canary"}},
		{args: []string{"grant", "create", "--constraint", "secret-canary"}},
	} {
		command := newRootCmd()
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		command.SetArgs(append(test.args, "--output", "json"))
		err := command.ExecuteContext(context.Background())
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err))
		assert.Empty(t, stdout.String())
		assert.NotContains(t, stderr.String(), "secret-canary")
	}
}

func idForSecurityTest() string { return "01ARZ3NDEKTSV4RRFFQ69G5FAV" }

func newPrincipalRequestETagServer(t *testing.T, id string) (*httptest.Server, chan principalRequestETagRecord) {
	t.Helper()
	requests := make(chan principalRequestETagRecord, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- principalRequestETagRecord{method: request.Method, path: request.URL.Path, etag: request.Header.Get("If-Match")}
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		if request.Method == http.MethodGet {
			if strings.HasPrefix(request.URL.Path, "/api/v1/principals/") {
				response.Header().Set("ETag", contract.PrincipalETag(id, "7"))
				_, _ = response.Write([]byte(principalETagBody(id, "7", true)))
				return
			}
			if strings.HasPrefix(request.URL.Path, "/api/v1/grants/") {
				response.Header().Set("ETag", contract.GrantETag(id, "1"))
				_, _ = response.Write([]byte(grantETagBody(id, "1", "Initial access")))
				return
			}
			response.Header().Set("ETag", contract.GrantRequestETag(id, "1"))
			_, _ = response.Write([]byte(grantRequestETagBody(id, "pending", "1", nil, nil)))
			return
		}
		if request.Method == http.MethodPatch || request.Method == http.MethodDelete {
			if strings.HasPrefix(request.URL.Path, "/api/v1/grants/") {
				response.Header().Set("ETag", contract.GrantETag(id, "2"))
				_, _ = response.Write([]byte(grantETagBody(id, "2", "Updated access")))
				return
			}
			response.Header().Set("ETag", contract.PrincipalETag(id, "8"))
			_, _ = response.Write([]byte(principalETagBody(id, "8", request.Method != http.MethodDelete)))
			return
		}
		var input map[string]json.RawMessage
		_ = json.Unmarshal(body, &input)
		response.Header().Set("ETag", contract.GrantRequestETag(id, "2"))
		if strings.HasSuffix(request.URL.Path, "/approve") {
			_, _ = response.Write([]byte(grantRequestETagBody(id, "approved", "2", input["approved_policy"], nil)))
			return
		}
		reason := input["reason"]
		_, _ = response.Write([]byte(grantRequestETagBody(id, "rejected", "2", nil, reason)))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func principalETagBody(id, revision string, occupied bool) string {
	credential := "null"
	if occupied {
		credential = `{"id":"` + id + `","fingerprint":"sha256:test","revision":"1","created_at":"2026-08-30T00:00:00Z"}`
	}
	return `{"id":"` + id + `","display_name":"Agent","state":"active","visibility":"requestable","revision":"` + revision + `","credential_revision":"1","credential":` + credential + `,"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z"}`
}

func grantETagBody(id, revision, description string) string {
	return `{"id":"` + id + `","description":"` + description + `","revision":"` + revision + `","principal_id":"` + id + `","effect":"allow","server_id":"` + id + `","upstream_name":null,"constraint":null,"expires_at":null,"state":"active","created_at":"2026-08-30T00:00:00Z"}`
}

func grantRequestETagBody(id, state, revision string, approvedPolicy, rejectionReason json.RawMessage) string {
	approved := "null"
	grantID := "null"
	reason := "null"
	if approvedPolicy != nil {
		approved = string(approvedPolicy)
		grantID = `"` + id + `"`
	}
	if rejectionReason != nil {
		reason = string(rejectionReason)
	}
	return `{"id":"` + id + `","principal_id":"` + id + `","state":"` + state + `","revision":"` + revision + `","requested_policy":{"scope":"tool","target":"example_tool","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false},"approved_policy":` + approved + `,"approved_grant_id":` + grantID + `,"rejection_reason":` + reason + `,"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z","closed_at":null,"resolved_server_id":"` + id + `","resolved_upstream_name":"example_tool","submitted_evidence":null,"approved_evidence":null,"current_target":{"scope":"tool","target_state":"available","active_state":null,"durable_state":null,"catalog_revision":null,"fingerprint":null,"descriptor":null}}`
}

func executePrincipalRequestETagCommand(t *testing.T, address string, args ...string) ([]byte, error) {
	t.Helper()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(testAdministratorBearer+"\n"), 0o600))
	command := newRootCmd()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetIn(bytes.NewReader(nil))
	base := []string{"--admin-bearer-file", bearerPath, "--address", address, "--output", "json"}
	command.SetArgs(append(args, base...))
	err := command.ExecuteContext(context.Background())
	if err != nil {
		return stderr.Bytes(), err
	}
	return stdout.Bytes(), nil
}
