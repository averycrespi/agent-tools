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

const directInputResourceID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type directInputRequest struct {
	path           string
	etag           string
	idempotencyKey string
	body           []byte
}

func TestCLIOperationStartDirectInput(t *testing.T) {
	server, requests := newDirectInputServer(t)
	bearerPath := writeDirectInputBearer(t)
	etag := contract.ServerETag(directInputResourceID, "1")

	output, err := executeDirectInputCommand(t, server.URL, bearerPath, "server", "operation", "start", directInputResourceID, "--etag", etag, "--kind", "retry", "--idempotency-key", "operation-key")
	require.NoError(t, err, "%s", output)
	request := <-requests
	assert.Equal(t, "/api/v1/servers/"+directInputResourceID+"/operations", request.path)
	assert.Equal(t, etag, request.etag)
	assert.Equal(t, "operation-key", request.idempotencyKey)
	assert.JSONEq(t, `{"kind":"retry"}`, string(request.body))

	output, err = executeDirectInputCommand(t, server.URL, bearerPath, "server", "operation", "start", directInputResourceID, "--etag", etag, "--kind", "reload", "--idempotency-key", "reload-key", "--yes")
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"kind":"reload"}`, string((<-requests).body))

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing kind", args: []string{"server", "operation", "start", directInputResourceID, "--etag", etag}},
		{name: "invalid kind", args: []string{"server", "operation", "start", directInputResourceID, "--etag", etag, "--kind", "activate"}},
		{name: "old file", args: []string{"server", "operation", "start", directInputResourceID, "--etag", etag, "--file", filepath.Join(t.TempDir(), "old.json")}},
		{name: "reload unconfirmed", args: []string{"server", "operation", "start", directInputResourceID, "--etag", etag, "--kind", "reload"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := executeDirectInputCommand(t, server.URL, bearerPath, test.args...)
			require.Error(t, err)
			assert.Equal(t, 2, commandExitCode(err), "%s", output)
			assert.Len(t, requests, 0)
		})
	}

	root := newRootCmd()
	command, _, err := root.Find([]string{"server", "operation", "start"})
	require.NoError(t, err)
	assert.NotNil(t, command.Flags().Lookup("kind"))
	assert.Nil(t, command.Flags().Lookup("file"))
	assert.NotContains(t, command.Example, "--file")
}

func TestCLIGrantRequestRejectDirectInput(t *testing.T) {
	server, requests := newDirectInputServer(t)
	bearerPath := writeDirectInputBearer(t)
	etag := contract.GrantRequestETag(directInputResourceID, "1")

	output, err := executeDirectInputCommand(t, server.URL, bearerPath, "grant-request", "reject", directInputResourceID, "--etag", etag, "--reason", "not_approved", "--yes")
	require.NoError(t, err, "%s", output)
	request := <-requests
	assert.Equal(t, "/api/v1/grant-requests/"+directInputResourceID+"/reject", request.path)
	assert.Equal(t, etag, request.etag)
	assert.JSONEq(t, `{"reason":"not_approved"}`, string(request.body))

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing reason", args: []string{"grant-request", "reject", directInputResourceID, "--etag", etag, "--yes"}},
		{name: "invalid reason", args: []string{"grant-request", "reject", directInputResourceID, "--etag", etag, "--reason", "denied", "--yes"}},
		{name: "old file", args: []string{"grant-request", "reject", directInputResourceID, "--etag", etag, "--file", filepath.Join(t.TempDir(), "old.json"), "--yes"}},
		{name: "unconfirmed", args: []string{"grant-request", "reject", directInputResourceID, "--etag", etag, "--reason", "policy_conflict"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := executeDirectInputCommand(t, server.URL, bearerPath, test.args...)
			require.Error(t, err)
			assert.Equal(t, 2, commandExitCode(err), "%s", output)
			assert.Len(t, requests, 0)
		})
	}

	root := newRootCmd()
	command, _, err := root.Find([]string{"grant-request", "reject"})
	require.NoError(t, err)
	assert.NotNil(t, command.Flags().Lookup("reason"))
	assert.Nil(t, command.Flags().Lookup("file"))
	assert.NotContains(t, command.Example, "--file")
}

func newDirectInputServer(t *testing.T) (*httptest.Server, chan directInputRequest) {
	t.Helper()
	requests := make(chan directInputRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- directInputRequest{path: request.URL.Path, etag: request.Header.Get("If-Match"), idempotencyKey: request.Header.Get("Idempotency-Key"), body: body}
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		if strings.Contains(request.URL.Path, "/operations") {
			var input contract.ServerOperationCreate
			if json.Unmarshal(body, &input) != nil {
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{"operation":{"id":"` + directInputResourceID + `","server_id":"` + directInputResourceID + `","kind":"` + string(input.Kind) + `","target_desired_revision":"1","target_credential_revisions":{},"state":"scheduled","reason":null,"created_at":"2026-08-30T00:00:00Z","started_at":null,"finished_at":null}}`))
			return
		}
		response.Header().Set("ETag", contract.GrantRequestETag(directInputResourceID, "2"))
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(`{"id":"` + directInputResourceID + `","principal_id":"` + directInputResourceID + `","state":"rejected","revision":"2","requested_policy":{"scope":"tool","target":"example","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false},"approved_policy":null,"approved_grant_id":null,"rejection_reason":"not_approved","created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z","closed_at":"2026-08-30T00:01:00Z","resolved_server_id":"` + directInputResourceID + `","resolved_upstream_name":"example","submitted_evidence":null,"approved_evidence":null,"current_target":{"scope":"tool","target_state":"available","active_state":null,"durable_state":null,"catalog_revision":null,"fingerprint":null,"descriptor":null}}`))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func writeDirectInputBearer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(path, []byte(testAdministratorBearer+"\n"), 0o600))
	return path
}

func executeDirectInputCommand(t *testing.T, address, bearerPath string, args ...string) ([]byte, error) {
	t.Helper()
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
