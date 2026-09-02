package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type credentialETagRequest struct {
	method string
	path   string
	etag   string
}

func TestCLICredentialAndAuthFlowETagModes(t *testing.T) {
	const resourceID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	t.Run("server credential replace", func(t *testing.T) {
		input := filepath.Join(t.TempDir(), "credential.json")
		require.NoError(t, os.WriteFile(input, []byte(`{"kind":"static_credential","expected_revision":"0","values":{"token":"secret"}}`), 0o600))
		for _, explicit := range []bool{false, true} {
			server, requests := newServerCredentialETagServer(t, resourceID)
			args := []string{"server", "credential", "replace", resourceID, "--file", input, "--yes"}
			if explicit {
				args = append(args, "--etag", contract.ServerETag(resourceID, "6"))
			}
			output, err := executeCredentialETagCommand(t, server.URL, args...)
			require.NoError(t, err, "%s", output)
			if explicit {
				require.Len(t, requests, 1)
				assert.Equal(t, contract.ServerETag(resourceID, "6"), (<-requests).etag)
			} else {
				require.Len(t, requests, 2)
				assert.Equal(t, http.MethodGet, (<-requests).method)
				assert.Equal(t, contract.ServerETag(resourceID, "7"), (<-requests).etag)
			}
		}
	})

	t.Run("server auth flow start", func(t *testing.T) {
		for _, explicit := range []bool{false, true} {
			server, requests := newServerCredentialETagServer(t, resourceID)
			command := &cobra.Command{}
			command.SetContext(context.Background())
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			terminal := &bufferWriteCloser{}
			options := &onlineOptions{address: strings.Replace(server.URL, "http://[::1]", "http://127.0.0.1", 1), output: string(controlclient.OutputJSON), adminBearer: onlineAdminBearer{value: testAdministratorBearer}}
			if explicit {
				options.etag = contract.ServerETag(resourceID, "6")
			}
			err := runServerAuthFlowStartWithTerminal(command, options, []string{resourceID}, func() (io.WriteCloser, error) { return terminal, nil })
			require.NoError(t, err)
			assert.Contains(t, terminal.String(), "https://example.test/authorize")
			if explicit {
				require.Len(t, requests, 1)
				assert.Equal(t, contract.ServerETag(resourceID, "6"), (<-requests).etag)
			} else {
				require.Len(t, requests, 2)
				assert.Equal(t, http.MethodGet, (<-requests).method)
				assert.Equal(t, contract.ServerETag(resourceID, "7"), (<-requests).etag)
			}
		}
	})

	t.Run("principal issue and rotate enforce slot intent", func(t *testing.T) {
		emptyServer, emptyRequests := newPrincipalCredentialIntentServer(t, resourceID, false)
		issuePath := filepath.Join(t.TempDir(), "issued")
		output, err := executeCredentialETagCommand(t, emptyServer.URL, "principal", "credential", "issue", resourceID, "--secret-output", issuePath, "--yes")
		require.NoError(t, err, "%s", output)
		require.Len(t, emptyRequests, 2)
		assert.Equal(t, http.MethodGet, (<-emptyRequests).method)
		issueRequest := <-emptyRequests
		assert.Equal(t, "/api/v1/principals/"+resourceID+"/credential", issueRequest.path)
		assert.Equal(t, contract.PrincipalETag(resourceID, "7"), issueRequest.etag)
		assert.FileExists(t, issuePath)

		occupiedServer, occupiedRequests := newPrincipalCredentialIntentServer(t, resourceID, true)
		rotatePath := filepath.Join(t.TempDir(), "rotated")
		output, err = executeCredentialETagCommand(t, occupiedServer.URL, "principal", "credential", "rotate", resourceID, "--etag", contract.PrincipalETag(resourceID, "7"), "--secret-output", rotatePath, "--yes")
		require.NoError(t, err, "%s", output)
		require.Len(t, occupiedRequests, 2)
		assert.Equal(t, http.MethodGet, (<-occupiedRequests).method)
		rotateRequest := <-occupiedRequests
		assert.Equal(t, "/api/v1/principals/"+resourceID+"/credential", rotateRequest.path)
		assert.Equal(t, contract.PrincipalETag(resourceID, "7"), rotateRequest.etag)
		assert.FileExists(t, rotatePath)

		for _, publication := range []struct {
			action   string
			occupied bool
			expected string
		}{
			{action: "issue", expected: "Do not replay issue"},
			{action: "rotate", occupied: true, expected: "prior bearer may already be invalid"},
		} {
			t.Run(publication.action+" publication failure", func(t *testing.T) {
				secretPath := filepath.Join(t.TempDir(), "publication-failure")
				server, requests := newPrincipalCredentialPublicationFailureServer(t, resourceID, publication.occupied, secretPath)
				output, commandErr := executeCredentialETagCommand(t, server.URL, "principal", "credential", publication.action, resourceID, "--secret-output", secretPath, "--yes")
				require.Error(t, commandErr)
				assert.Equal(t, 2, commandExitCode(commandErr), "%s", output)
				assert.Contains(t, string(output), publication.expected)
				assert.NotContains(t, string(output), "mgw_agent_")
				assert.Len(t, requests, 2)
				preserved, readErr := os.ReadFile(secretPath)
				require.NoError(t, readErr)
				assert.Equal(t, "untrusted replacement", string(preserved))
			})
		}

		for _, mismatch := range []struct {
			name     string
			occupied bool
			command  string
			etag     string
		}{
			{name: "issue occupied", occupied: true, command: "issue"},
			{name: "rotate empty", occupied: false, command: "rotate"},
			{name: "explicit mismatch", occupied: false, command: "issue", etag: contract.PrincipalETag(resourceID, "6")},
		} {
			t.Run(mismatch.name, func(t *testing.T) {
				server, requests := newPrincipalCredentialIntentServer(t, resourceID, mismatch.occupied)
				secretPath := filepath.Join(t.TempDir(), "unused")
				args := []string{"principal", "credential", mismatch.command, resourceID, "--secret-output", secretPath, "--yes"}
				if mismatch.etag != "" {
					args = append(args, "--etag", mismatch.etag)
				}
				output, err := executeCredentialETagCommand(t, server.URL, args...)
				require.Error(t, err)
				assert.Equal(t, 2, commandExitCode(err), "%s", output)
				require.Len(t, requests, 1)
				assert.Equal(t, http.MethodGet, (<-requests).method)
				assert.NoFileExists(t, secretPath)
			})
		}
	})

	root := newRootCmd()
	rotate, _, err := root.Find([]string{"principal", "credential", "rotate"})
	require.NoError(t, err)
	assert.NotNil(t, rotate.Flags().Lookup("etag"))
	assert.NotNil(t, rotate.Flags().Lookup("secret-output"))
}

func newServerCredentialETagServer(t *testing.T, id string) (*httptest.Server, chan credentialETagRequest) {
	t.Helper()
	requests := make(chan credentialETagRequest, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		requests <- credentialETagRequest{method: request.Method, path: request.URL.Path, etag: request.Header.Get("If-Match")}
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		if request.Method == http.MethodGet {
			response.Header().Set("ETag", contract.ServerETag(id, "7"))
			_, _ = response.Write([]byte(`{"id":"` + id + `","desired_revision":"7"}`))
			return
		}
		if strings.HasSuffix(request.URL.Path, "/credential-replacements") {
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{"server_id":"` + id + `","kind":"static_credential","credential_revision":"1","operation":{"id":"` + id + `","server_id":"` + id + `","kind":"credential_replace","target_desired_revision":"7","target_credential_revisions":{},"state":"scheduled","reason":null,"created_at":"2026-08-30T00:00:00Z","started_at":null,"finished_at":null}}`))
			return
		}
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"flow":{"id":"` + id + `","server_id":"` + id + `","flow_state":"awaiting_callback","target_desired_revision":"7","registration_revision":"1","created_at":"2026-08-30T00:00:00Z","expires_at":"2026-08-30T01:00:00Z","finished_at":null,"reason":null},"authorization_url":"https://example.test/authorize"}`))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func newPrincipalCredentialIntentServer(t *testing.T, id string, occupied bool) (*httptest.Server, chan credentialETagRequest) {
	t.Helper()
	requests := make(chan credentialETagRequest, 3)
	credential := "null"
	if occupied {
		credential = `{"id":"` + id + `","fingerprint":"sha256:old","revision":"1","created_at":"2026-08-30T00:00:00Z"}`
	}
	principal := `{"id":"` + id + `","display_name":"Agent","state":"active","visibility":"requestable","revision":"7","credential_revision":"1","credential":` + credential + `,"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:00:00Z"}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		requests <- credentialETagRequest{method: request.Method, path: request.URL.Path, etag: request.Header.Get("If-Match")}
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		if request.Method == http.MethodGet {
			response.Header().Set("ETag", contract.PrincipalETag(id, "7"))
			_, _ = response.Write([]byte(principal))
			return
		}
		response.Header().Set("ETag", contract.PrincipalETag(id, "8"))
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"principal":{"id":"` + id + `","display_name":"Agent","state":"active","visibility":"requestable","revision":"8","credential_revision":"2","credential":{"id":"` + id + `","fingerprint":"sha256:new","revision":"2","created_at":"2026-08-30T00:01:00Z"},"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z"},"bearer":"mgw_agent_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func newPrincipalCredentialPublicationFailureServer(t *testing.T, id string, occupied bool, secretPath string) (*httptest.Server, chan credentialETagRequest) {
	t.Helper()
	requests := make(chan credentialETagRequest, 2)
	credential := "null"
	if occupied {
		credential = `{"id":"` + id + `","fingerprint":"sha256:old","revision":"1","created_at":"2026-08-30T00:00:00Z"}`
	}
	principal := `{"id":"` + id + `","display_name":"Agent","state":"active","visibility":"requestable","revision":"7","credential_revision":"1","credential":` + credential + `,"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:00:00Z"}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		requests <- credentialETagRequest{method: request.Method, path: request.URL.Path, etag: request.Header.Get("If-Match")}
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		if request.Method == http.MethodGet {
			response.Header().Set("ETag", contract.PrincipalETag(id, "7"))
			_, _ = response.Write([]byte(principal))
			return
		}
		if removeErr := os.Remove(secretPath); removeErr != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if writeErr := os.WriteFile(secretPath, []byte("untrusted replacement"), 0o600); writeErr != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Header().Set("ETag", contract.PrincipalETag(id, "8"))
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"principal":{"id":"` + id + `","display_name":"Agent","state":"active","visibility":"requestable","revision":"8","credential_revision":"2","credential":{"id":"` + id + `","fingerprint":"sha256:new","revision":"2","created_at":"2026-08-30T00:01:00Z"},"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z"},"bearer":"mgw_agent_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func executeCredentialETagCommand(t *testing.T, address string, args ...string) ([]byte, error) {
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

type bufferWriteCloser struct {
	bytes.Buffer
}

func (writer *bufferWriteCloser) Close() error { return nil }
