package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type serverMutationETagRequest struct {
	method string
	path   string
	etag   string
}

func TestCLIServerMutationETagModes(t *testing.T) {
	const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	cases := []struct {
		name string
		args []string
	}{
		{name: "update", args: []string{"server", "update", serverID, "--display-name", "Renamed"}},
		{name: "delete", args: []string{"server", "delete", serverID, "--yes"}},
		{name: "operation start", args: []string{"server", "operation", "start", serverID, "--kind", "retry", "--idempotency-key", "etag-mode-key"}},
	}
	for _, test := range cases {
		t.Run(test.name+"/omitted", func(t *testing.T) {
			server, requests := newServerMutationETagServer(t, serverID, false, false)
			output, err := executeServerMutationETagCommand(t, server.URL, test.args...)
			require.NoError(t, err, "%s", output)
			require.Len(t, requests, 2)
			read := <-requests
			mutation := <-requests
			assert.Equal(t, serverMutationETagRequest{method: http.MethodGet, path: "/api/v1/servers/" + serverID}, read)
			assert.Equal(t, contract.ServerETag(serverID, "7"), mutation.etag)
		})
		t.Run(test.name+"/explicit", func(t *testing.T) {
			server, requests := newServerMutationETagServer(t, serverID, false, false)
			explicit := contract.ServerETag(serverID, "6")
			output, err := executeServerMutationETagCommand(t, server.URL, append(test.args, "--etag", explicit)...)
			require.NoError(t, err, "%s", output)
			require.Len(t, requests, 1)
			mutation := <-requests
			assert.NotEqual(t, http.MethodGet, mutation.method)
			assert.Equal(t, explicit, mutation.etag)
		})
	}

	t.Run("omitted conflict does not refetch or replay", func(t *testing.T) {
		server, requests := newServerMutationETagServer(t, serverID, true, false)
		output, err := executeServerMutationETagCommand(t, server.URL, cases[0].args...)
		require.Error(t, err)
		assert.Contains(t, string(output), `"code":"stale_revision"`)
		require.Len(t, requests, 2)
		assert.Equal(t, http.MethodGet, (<-requests).method)
		assert.Equal(t, http.MethodPatch, (<-requests).method)
	})

	t.Run("explicit conflict does not fetch or replay", func(t *testing.T) {
		server, requests := newServerMutationETagServer(t, serverID, true, false)
		output, err := executeServerMutationETagCommand(t, server.URL, append(cases[0].args, "--etag", contract.ServerETag(serverID, "6"))...)
		require.Error(t, err)
		assert.Contains(t, string(output), `"code":"stale_revision"`)
		require.Len(t, requests, 1)
		assert.Equal(t, http.MethodPatch, (<-requests).method)
	})

	t.Run("invalid preflight submits nothing", func(t *testing.T) {
		server, requests := newServerMutationETagServer(t, serverID, false, true)
		output, err := executeServerMutationETagCommand(t, server.URL, cases[0].args...)
		require.Error(t, err)
		assert.Equal(t, 10, commandExitCode(err))
		assert.Contains(t, string(output), `"code":"client_response_invalid"`)
		assert.Contains(t, string(output), `"uncertain":false`)
		require.Len(t, requests, 1)
		assert.Equal(t, http.MethodGet, (<-requests).method)
	})
}

func newServerMutationETagServer(t *testing.T, serverID string, conflict, invalidRead bool) (*httptest.Server, chan serverMutationETagRequest) {
	t.Helper()
	requests := make(chan serverMutationETagRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		requests <- serverMutationETagRequest{method: request.Method, path: request.URL.Path, etag: request.Header.Get("If-Match")}
		if request.Method == http.MethodGet {
			response.Header().Set("Content-Type", controlclient.MediaTypeJSON)
			response.Header().Set("ETag", contract.ServerETag(serverID, "7"))
			if invalidRead {
				_, _ = response.Write([]byte(`{"id":`))
				return
			}
			_, _ = response.Write([]byte(`{"id":"` + serverID + `","desired_revision":"7"}`))
			return
		}
		if conflict {
			response.Header().Set("Content-Type", controlclient.MediaTypeProblemJSON)
			response.WriteHeader(http.StatusPreconditionFailed)
			_, _ = response.Write([]byte(`{"status":412,"code":"stale_revision","title":"The server revision is stale."}`))
			return
		}
		if request.Method == http.MethodPost {
			parts := serverETagPattern.FindStringSubmatch(request.Header.Get("If-Match"))
			revision := "0"
			if len(parts) == 3 {
				revision = parts[2]
			}
			response.Header().Set("Content-Type", controlclient.MediaTypeJSON)
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{"operation":{"id":"` + serverID + `","server_id":"` + serverID + `","kind":"retry","target_desired_revision":"` + revision + `","target_credential_revisions":{},"state":"scheduled","reason":null,"created_at":"2026-08-30T00:00:00Z","started_at":null,"finished_at":null}}`))
			return
		}
		response.Header().Set("Content-Type", controlclient.MediaTypeJSON)
		response.Header().Set("ETag", contract.ServerETag(serverID, "8"))
		response.WriteHeader(http.StatusOK)
		if request.Method == http.MethodDelete {
			_, _ = response.Write([]byte(`{"server":{"id":"` + serverID + `","desired_state":"deleted","desired_revision":"8","transport":null},"operation":{"id":"` + serverID + `","server_id":"` + serverID + `","kind":"delete","target_desired_revision":"7","target_credential_revisions":{},"state":"scheduled","reason":null,"created_at":"2026-08-30T00:00:00Z","started_at":null,"finished_at":null}}`))
			return
		}
		_, _ = response.Write([]byte(`{"server":{"id":"` + serverID + `","desired_revision":"8"},"operation":null}`))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func executeServerMutationETagCommand(t *testing.T, address string, args ...string) ([]byte, error) {
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
