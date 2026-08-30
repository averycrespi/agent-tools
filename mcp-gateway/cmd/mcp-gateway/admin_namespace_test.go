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

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIAdminNamespaceAndCredentialCreate(t *testing.T) {
	root := newRootCmd()
	adminCommand, _, err := root.Find([]string{"admin"})
	require.NoError(t, err)
	credentialCommand, _, err := root.Find([]string{"admin", "credential"})
	require.NoError(t, err)
	createCommand, _, err := root.Find([]string{"admin", "credential", "create"})
	require.NoError(t, err)
	resetCommand, _, err := root.Find([]string{"admin", "reset"})
	require.NoError(t, err)
	assert.Contains(t, adminCommand.Short, "administrator")
	assert.Contains(t, credentialCommand.Short, "credential")
	assert.Contains(t, resetCommand.Short, "all administrator authority")
	assert.NotNil(t, createCommand.Flags().Lookup("expires-at"))
	assert.Nil(t, createCommand.Flags().Lookup("file"))
	assert.Nil(t, resetCommand.Flags().Lookup("address"))
	assert.Nil(t, resetCommand.Flags().Lookup("admin-bearer-file"))
	assert.Empty(t, adminCommand.Aliases)
	assert.Empty(t, credentialCommand.Aliases)
	_, _, err = root.Find([]string{"admin-reset"})
	require.Error(t, err)
	_, _, err = root.Find([]string{"admin-credential"})
	require.Error(t, err)

	requests := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- body
		response.Header().Set("Content-Type", controlclient.MediaTypeJSON)
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","fingerprint":"sha256:created","created_at":"2026-08-30T00:00:00Z","expires_at":null,"non_expiring":true,"status":"active","revision":"1","bearer":"mgw_admin_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	bearerPath := filepath.Join(dir, "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(testAdministratorBearer+"\n"), 0o600))
	executeCreate := func(t *testing.T, secretPath string, extra ...string) ([]byte, error) {
		t.Helper()
		command := newRootCmd()
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		args := []string{"admin", "credential", "create", "--secret-output", secretPath, "--admin-bearer-file", bearerPath, "--address", server.URL, "--output", "json"}
		args = append(args, extra...)
		command.SetArgs(args)
		err := command.ExecuteContext(context.Background())
		if err != nil {
			return stderr.Bytes(), err
		}
		assert.NotContains(t, stdout.String(), "mgw_admin_")
		return stdout.Bytes(), nil
	}

	nonExpiringPath := filepath.Join(dir, "non-expiring")
	output, err := executeCreate(t, nonExpiringPath)
	require.NoError(t, err, "%s", output)
	secretInfo, err := os.Stat(nonExpiringPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), secretInfo.Mode().Perm())
	published, err := os.ReadFile(nonExpiringPath)
	require.NoError(t, err)
	assert.Equal(t, "mgw_admin_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD\n", string(published))
	assert.JSONEq(t, `{"expires_at":null}`, string(<-requests))
	assert.JSONEq(t, `{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","fingerprint":"sha256:created","created_at":"2026-08-30T00:00:00Z","expires_at":null,"non_expiring":true,"status":"active","revision":"1"}`, string(output))

	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
	output, err = executeCreate(t, filepath.Join(dir, "expiring"), "--expires-at", expiresAt)
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"expires_at":"`+expiresAt+`"}`, string(<-requests))

	missingBearer := filepath.Join(dir, "missing")
	invalid := newRootCmd()
	var stderr bytes.Buffer
	invalid.SetErr(&stderr)
	invalid.SetOut(io.Discard)
	invalid.SetArgs([]string{"admin", "credential", "create", "--expires-at", "not-a-time", "--secret-output", filepath.Join(dir, "unused"), "--admin-bearer-file", missingBearer, "--address", server.URL, "--output", "json"})
	err = invalid.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Contains(t, stderr.String(), `"code":"client_invalid_input"`)
	assert.Len(t, requests, 0)

	oldFile := newRootCmd()
	stderr.Reset()
	oldFile.SetErr(&stderr)
	oldFile.SetOut(io.Discard)
	oldFile.SetArgs([]string{"admin", "credential", "create", "--file", filepath.Join(dir, "old.json"), "--secret-output", filepath.Join(dir, "unused-2"), "--admin-bearer-file", missingBearer, "--address", server.URL, "--output", "json"})
	err = oldFile.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Len(t, requests, 0)
}
