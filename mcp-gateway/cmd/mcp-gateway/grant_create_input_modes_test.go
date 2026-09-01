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
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIGrantCreateInputModes(t *testing.T) {
	const resourceID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	requests := make(chan []byte, 6)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- body
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"id":"` + resourceID + `","name":"Test grant","principal_id":"` + resourceID + `","effect":"allow","server_id":"` + resourceID + `","upstream_name":null,"constraint":null,"expires_at":null,"state":"active","created_at":"2026-08-30T00:00:00Z"}`))
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
	directArgs := []string{"grant", "create", "--name", "Test grant", "--principal-id", resourceID, "--effect", "allow", "--server-id", resourceID}
	baseBody := `{"name":"Test grant","principal_id":"` + resourceID + `","effect":"allow","server_id":"` + resourceID + `","upstream_name":null,"constraint":null,"expires_at":null}`

	output, err := execute(directArgs...)
	require.NoError(t, err, "%s", output)
	directBody := <-requests
	assert.JSONEq(t, baseBody, string(directBody))

	baseFile := filepath.Join(dir, "base.json")
	require.NoError(t, os.WriteFile(baseFile, []byte(baseBody), 0o600))
	output, err = execute("grant", "create", "--file", baseFile)
	require.NoError(t, err, "%s", output)
	assert.Equal(t, string(directBody), string(<-requests))

	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
	output, err = execute(append(directArgs, "--upstream-name", "example_tool", "--expires-at", expiresAt)...)
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"name":"Test grant","principal_id":"`+resourceID+`","effect":"allow","server_id":"`+resourceID+`","upstream_name":"example_tool","constraint":null,"expires_at":"`+expiresAt+`"}`, string(<-requests))

	constrainedBody := `{"name":"Test grant","principal_id":"` + resourceID + `","effect":"deny","server_id":"` + resourceID + `","upstream_name":"example_tool","constraint":{"equals":{"/region":"us"}},"expires_at":null}`
	constrainedFile := filepath.Join(dir, "constrained.json")
	require.NoError(t, os.WriteFile(constrainedFile, []byte(constrainedBody), 0o600))
	output, err = execute("grant", "create", "--file", constrainedFile)
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, constrainedBody, string(<-requests))

	incompleteFile := filepath.Join(dir, "incomplete.json")
	require.NoError(t, os.WriteFile(incompleteFile, []byte(`{"principal_id":"`+resourceID+`"}`), 0o600))
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing name", args: []string{"grant", "create", "--principal-id", resourceID, "--effect", "allow", "--server-id", resourceID}},
		{name: "missing principal", args: []string{"grant", "create", "--name", "Test grant", "--effect", "allow", "--server-id", resourceID}},
		{name: "invalid effect", args: []string{"grant", "create", "--name", "Test grant", "--principal-id", resourceID, "--effect", "audit", "--server-id", resourceID}},
		{name: "invalid server", args: []string{"grant", "create", "--name", "Test grant", "--principal-id", resourceID, "--effect", "allow", "--server-id", "bad"}},
		{name: "empty upstream", args: append(directArgs, "--upstream-name", "")},
		{name: "invalid expiry", args: append(directArgs, "--expires-at", "tomorrow")},
		{name: "mixed", args: append(directArgs, "--file", baseFile)},
		{name: "incomplete file", args: []string{"grant", "create", "--file", incompleteFile}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := execute(test.args...)
			require.Error(t, err)
			assert.Equal(t, 2, commandExitCode(err), "%s", output)
			assert.Len(t, requests, 0)
		})
	}

	root := newRootCmd()
	command, _, err := root.Find([]string{"grant", "create"})
	require.NoError(t, err)
	for _, flag := range []string{"name", "principal-id", "effect", "server-id", "upstream-name", "expires-at", "file"} {
		assert.NotNil(t, command.Flags().Lookup(flag), flag)
	}
	assert.Contains(t, command.Example, "--principal-id")
	assert.Contains(t, command.Example, "--file")
}
