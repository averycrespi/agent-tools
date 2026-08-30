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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIPrincipalDirectInput(t *testing.T) {
	const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	type capturedRequest struct {
		method string
		path   string
		etag   string
		body   []byte
	}
	requests := make(chan capturedRequest, 4)
	principalBody := `{"id":"` + principalID + `","display_name":"Direct agent","state":"active","visibility":"requestable","revision":"2","credential_revision":"1","credential":null,"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z"}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- capturedRequest{method: request.Method, path: request.URL.Path, etag: request.Header.Get("If-Match"), body: body}
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		response.Header().Set("ETag", contract.PrincipalETag(principalID, "2"))
		if request.Method == http.MethodPost {
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"principal":` + principalBody + `,"default_grant":{"principal_id":"` + principalID + `"}}`))
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(principalBody))
	}))
	defer server.Close()

	dir := t.TempDir()
	bearerPath := filepath.Join(dir, "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(testAdministratorBearer+"\n"), 0o600))
	execute := func(args ...string) ([]byte, error) {
		command := newRootCmd()
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		command.SetIn(bytes.NewReader(nil))
		base := []string{"--admin-bearer-file", bearerPath, "--address", server.URL, "--output", "json"}
		command.SetArgs(append(args, base...))
		err := command.ExecuteContext(context.Background())
		if err != nil {
			return stderr.Bytes(), err
		}
		return stdout.Bytes(), nil
	}

	output, err := execute("principal", "create", "--display-name", "Direct agent", "--visibility", "requestable")
	require.NoError(t, err, "%s", output)
	created := <-requests
	assert.Equal(t, http.MethodPost, created.method)
	assert.Equal(t, "/api/v1/principals", created.path)
	assert.Empty(t, created.etag)
	assert.JSONEq(t, `{"display_name":"Direct agent","visibility":"requestable"}`, string(created.body))

	etag := contract.PrincipalETag(principalID, "1")
	output, err = execute("principal", "update", principalID, "--etag", etag, "--display-name", "Renamed")
	require.NoError(t, err, "%s", output)
	updated := <-requests
	assert.Equal(t, http.MethodPatch, updated.method)
	assert.Equal(t, "/api/v1/principals/"+principalID, updated.path)
	assert.Equal(t, etag, updated.etag)
	assert.JSONEq(t, `{"display_name":"Renamed"}`, string(updated.body))

	output, err = execute("principal", "update", principalID, "--etag", etag, "--visibility", "allowed-only")
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"visibility":"allowed-only"}`, string((<-requests).body))

	output, err = execute("principal", "update", principalID, "--etag", etag, "--state", "disabled", "--yes")
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"state":"disabled"}`, string((<-requests).body))

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "create missing required flag", args: []string{"principal", "create", "--visibility", "requestable"}},
		{name: "create old file", args: []string{"principal", "create", "--file", filepath.Join(dir, "old.json")}},
		{name: "update empty", args: []string{"principal", "update", principalID, "--etag", etag}},
		{name: "update invalid visibility", args: []string{"principal", "update", principalID, "--etag", etag, "--visibility", "private"}},
		{name: "update state unconfirmed", args: []string{"principal", "update", principalID, "--etag", etag, "--state", "disabled"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := execute(test.args...)
			require.Error(t, err)
			assert.Equal(t, 2, commandExitCode(err), "%s", output)
			assert.Len(t, requests, 0)
		})
	}

	root := newRootCmd()
	create, _, err := root.Find([]string{"principal", "create"})
	require.NoError(t, err)
	update, _, err := root.Find([]string{"principal", "update"})
	require.NoError(t, err)
	assert.NotNil(t, create.Flags().Lookup("display-name"))
	assert.NotNil(t, create.Flags().Lookup("visibility"))
	assert.Nil(t, create.Flags().Lookup("file"))
	assert.NotNil(t, update.Flags().Lookup("display-name"))
	assert.NotNil(t, update.Flags().Lookup("visibility"))
	assert.NotNil(t, update.Flags().Lookup("state"))
	assert.Nil(t, update.Flags().Lookup("file"))
	assert.NotContains(t, create.Example, "--file")
	assert.NotContains(t, update.Example, "--file")
}
