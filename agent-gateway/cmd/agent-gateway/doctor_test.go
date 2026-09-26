package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorAbsentAndPartialResultsAreReadOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/readyz", r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
	}))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "missing")
	run := func() doctorResult {
		command := newRootCmd()
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs([]string{"doctor", "--data-dir", root, "--address", server.URL, "--json"})
		require.NoError(t, command.ExecuteContext(t.Context()), stderr.String())
		var result doctorResult
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
		require.Equal(t, "--data-dir", result.Selection)
		require.Equal(t, root, result.DataDir)
		return result
	}
	result := run()
	require.Equal(t, "absent", result.Checks[0].State)
	_, err := os.Stat(root)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "admin-bearer"), []byte("private-test-value"), 0600))
	before := treeBytes(t, root)
	result = run()
	require.Equal(t, before, treeBytes(t, root))
	data, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-test-value")
	states := map[string]string{}
	for _, check := range result.Checks {
		states[check.Name] = check.State
	}
	require.Equal(t, "present", states["administrator credential"])
	require.Equal(t, "absent", states["storage"])
	require.Equal(t, "not-ready", states["runtime readiness"])
	require.Equal(t, "not-checked", states["protected CA signing material"])
}

func TestDoctorDoesNotReadDefaultBearerThroughUnsafeRoot(t *testing.T) {
	var authenticated atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authenticated.Add(1)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "admin-bearer"), []byte(testAdministratorBearer+"\n"), 0600))
	root := filepath.Join(t.TempDir(), "linked")
	require.NoError(t, os.Symlink(target, root))
	command := newRootCmd()
	stdout := new(bytes.Buffer)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"doctor", "--data-dir", root, "--address", server.URL, "--online", "--json"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Zero(t, authenticated.Load())
	require.NotContains(t, stdout.String(), testAdministratorBearer)
	var result doctorResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	states := map[string]string{}
	for _, check := range result.Checks {
		states[check.Name] = check.State
	}
	require.Equal(t, "failed", states["installation"])
	require.Equal(t, "not-checked", states["administrator credential"])
	require.Equal(t, "not-checked", states["authenticated live status"])
	require.Equal(t, "failed", states["runtime readiness"])
}
