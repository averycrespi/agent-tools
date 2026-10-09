package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorExplainsReadinessAndAuthenticationAtCLISink(t *testing.T) {
	for _, mode := range []string{"human", "verbose", "json"} {
		t.Run(mode, func(t *testing.T) {
			var authenticated atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					authenticated.Add(1)
				}
				if r.URL.Path == "/readyz" {
					w.WriteHeader(http.StatusBadGateway)
					_, _ = w.Write([]byte(`{"message":"private-untrusted-body\u001b"}`))
					return
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"code":"unauthorized","title":"private-untrusted-body"}`))
			}))
			defer server.Close()
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0700))
			bearerPath := filepath.Join(root, "selected-bearer")
			require.NoError(t, os.WriteFile(bearerPath, []byte(testAdministratorBearer+"\n"), 0600))
			command := newRootCmd()
			output := new(bytes.Buffer)
			command.SetOut(output)
			command.SetErr(new(bytes.Buffer))
			args := []string{"doctor", "--data-dir", root, "--address", server.URL, "--online", "--admin-bearer-file", bearerPath}
			if mode == "json" {
				args = append(args, "--json")
			}
			if mode == "verbose" {
				args = append(args, "--verbose")
			}
			command.SetArgs(args)
			require.NoError(t, command.ExecuteContext(t.Context()))
			require.Contains(t, output.String(), "http_status=502")
			require.Contains(t, output.String(), "http_status=401")
			require.Contains(t, output.String(), "problem_code=unauthorized")
			require.Contains(t, output.String(), "authentication=attempted")
			require.Contains(t, output.String(), server.URL)
			require.NotContains(t, output.String(), testAdministratorBearer)
			require.NotContains(t, output.String(), "private-untrusted-body")
			require.EqualValues(t, 1, authenticated.Load())
		})
	}
}

func TestDoctorExpiredContextDoesNotImplyAuthentication(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	bearerPath := filepath.Join(root, "selected-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(testAdministratorBearer+"\n"), 0600))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	command := newRootCmd()
	output := new(bytes.Buffer)
	command.SetOut(output)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"doctor", "--data-dir", root, "--address", server.URL, "--online", "--admin-bearer-file", bearerPath})
	require.NoError(t, command.ExecuteContext(ctx))
	require.Contains(t, output.String(), "shared doctor context expired")
	require.Contains(t, output.String(), "authentication=not_attempted")
	require.Contains(t, output.String(), "context canceled")
	require.NotContains(t, output.String(), testAdministratorBearer)
	require.Zero(t, requests.Load())
}

func TestDoctorMalformedReadinessRetainsStatusNotBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("malformed-private-body\x1b"))
	}))
	defer server.Close()
	command := newRootCmd()
	output := new(bytes.Buffer)
	command.SetOut(output)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"doctor", "--data-dir", filepath.Join(t.TempDir(), "absent"), "--address", server.URL})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Contains(t, output.String(), "http_status=502")
	require.Contains(t, output.String(), "expected bounded unique-member JSON")
	require.NotContains(t, output.String(), "malformed-private-body")
}

func TestDoctorUnavailableCauseIsVisibleWithoutVerbose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()
	command := newRootCmd()
	output := new(bytes.Buffer)
	command.SetOut(output)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"doctor", "--data-dir", filepath.Join(t.TempDir(), "absent"), "--address", address})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Contains(t, output.String(), "connection refused")
	require.Contains(t, output.String(), address)
}
