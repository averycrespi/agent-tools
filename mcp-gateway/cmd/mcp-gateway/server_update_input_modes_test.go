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

func TestCLIServerUpdateInputModes(t *testing.T) {
	const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	requests := make(chan []byte, 8)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- body
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		response.Header().Set("ETag", contract.ServerETag(serverID, "2"))
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(`{"server":{"id":"` + serverID + `","desired_revision":"2"},"operation":null}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	bearerPath := filepath.Join(dir, "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(testAdministratorBearer+"\n"), 0o600))
	etag := contract.ServerETag(serverID, "1")
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
	updateArgs := []string{"server", "update", serverID, "--etag", etag}

	output, err := execute(append(updateArgs, "--display-name", "Renamed")...)
	require.NoError(t, err, "%s", output)
	directDisplay := <-requests
	assert.JSONEq(t, `{"display_name":"Renamed"}`, string(directDisplay))

	displayFile := filepath.Join(dir, "display.json")
	require.NoError(t, os.WriteFile(displayFile, []byte("{\n  \"display_name\": \"Renamed\"\n}\n"), 0o600))
	output, err = execute(append(updateArgs, "--file", displayFile)...)
	require.NoError(t, err, "%s", output)
	assert.Equal(t, string(directDisplay), string(<-requests))

	output, err = execute(append(updateArgs, "--enable", "--yes")...)
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"enabled":true}`, string(<-requests))

	output, err = execute(append(updateArgs, "--disable", "--yes")...)
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"enabled":false}`, string(<-requests))

	fullPatch := `{"display_name":"Remote","enabled":true,"transport":{"kind":"streamable_http","url":"https://example.test/mcp","protocol_mode":"auto","authentication":{"mode":"none"}}}`
	fullFile := filepath.Join(dir, "full.json")
	require.NoError(t, os.WriteFile(fullFile, []byte(fullPatch), 0o600))
	output, err = execute(append(updateArgs, "--file", fullFile, "--yes")...)
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, fullPatch, string(<-requests))

	emptyFile := filepath.Join(dir, "empty.json")
	require.NoError(t, os.WriteFile(emptyFile, []byte(`{}`), 0o600))
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "empty direct", args: updateArgs},
		{name: "mixed", args: append(updateArgs, "--file", displayFile, "--display-name", "Other")},
		{name: "enable disable conflict", args: append(updateArgs, "--enable", "--disable", "--yes")},
		{name: "empty file", args: append(updateArgs, "--file", emptyFile)},
		{name: "enable unconfirmed", args: append(updateArgs, "--enable")},
		{name: "transport unconfirmed", args: append(updateArgs, "--file", fullFile)},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := execute(test.args...)
			require.Error(t, err)
			assert.Equal(t, 2, commandExitCode(err), "%s", output)
			assert.Len(t, requests, 0)
		})
	}

	root := newRootCmd()
	command, _, err := root.Find([]string{"server", "update"})
	require.NoError(t, err)
	assert.NotNil(t, command.Flags().Lookup("display-name"))
	assert.NotNil(t, command.Flags().Lookup("enable"))
	assert.NotNil(t, command.Flags().Lookup("disable"))
	assert.NotNil(t, command.Flags().Lookup("file"))
	assert.Contains(t, command.Example, "--display-name")
	assert.Contains(t, command.Example, "--file")
}
