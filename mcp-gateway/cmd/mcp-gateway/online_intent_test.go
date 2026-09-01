package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLILocalIntentPrecedesAuthority(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	requests := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	server.Listener = listener
	server.Start()
	defer server.Close()

	assertRejectedBeforeAuthority := func(t *testing.T, args []string) string {
		t.Helper()
		reader := &countingReader{reader: bytes.NewBufferString(testAdministratorBearer + "\n")}
		root := newRootCmd()
		var stderr bytes.Buffer
		root.SetIn(reader)
		root.SetErr(&stderr)
		root.SetOut(io.Discard)
		root.SetArgs(append(args, "--admin-bearer-stdin", "--address", server.URL, "--output", "json"))
		err := root.Execute()
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err))
		assert.Zero(t, reader.reads)
		assert.Zero(t, requests)
		assert.Contains(t, stderr.String(), `"code":"client_invalid_input"`)
		return stderr.String()
	}

	root := t.TempDir()
	malformed := filepath.Join(root, "malformed.json")
	require.NoError(t, os.WriteFile(malformed, []byte(`{"namespace":`), 0o600))
	assert.Contains(t, assertRejectedBeforeAuthority(t, []string{"server", "create", "--file", malformed}), "file input")
	semantic := filepath.Join(root, "semantic.json")
	require.NoError(t, os.WriteFile(semantic, []byte(`{}`), 0o600))
	assert.Contains(t, assertRejectedBeforeAuthority(t, []string{"server", "create", "--file", semantic}), "file input")

	patch := filepath.Join(root, "patch.json")
	require.NoError(t, os.WriteFile(patch, []byte(`{"display_name":"new"}`), 0o600))
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	assert.Contains(t, assertRejectedBeforeAuthority(t, []string{"server", "update", id, "--etag", `"server-` + id + `-1"`, "--file", patch, "--display-name", "other"}), "either direct input flags or --file")
	assert.Contains(t, assertRejectedBeforeAuthority(t, []string{"server", "operation", "start", id, "--kind", "invented"}), "--kind value is invalid")
	assert.Contains(t, assertRejectedBeforeAuthority(t, []string{"principal", "create", "--visibility", "requestable"}), "--display-name flag is required")
	assert.Contains(t, assertRejectedBeforeAuthority(t, []string{"server", "get", "not-an-id"}), "resource ID is invalid")

	stdinConflict := &countingReader{reader: bytes.NewBufferString(`{"namespace":"x"}`)}
	command := newRootCmd()
	var stderr bytes.Buffer
	command.SetIn(stdinConflict)
	command.SetErr(&stderr)
	command.SetOut(io.Discard)
	command.SetArgs([]string{"server", "create", "--file", "-", "--admin-bearer-stdin", "--output", "json"})
	err = command.Execute()
	require.Error(t, err)
	assert.Zero(t, stdinConflict.reads)
	assert.Contains(t, stderr.String(), "Standard input cannot provide both")

	declarations := map[string][]string{
		"admin credential create": {"expires-at"},
		"server update":           {"display-name", "enable", "disable"},
		"server operation start":  {"kind"},
		"principal create":        {"display-name", "visibility"},
		"principal update":        {"display-name", "visibility", "state"},
		"grant create":            {"name", "principal-id", "effect", "server-id", "upstream-name", "expires-at"},
		"grant-request approve":   {"name", "scope", "target", "duration-seconds", "acknowledge-future-tools"},
		"grant-request reject":    {"reason"},
	}
	command = newRootCmd()
	for path, flags := range declarations {
		leaf, _, findErr := command.Find(strings.Fields(path))
		require.NoError(t, findErr, path)
		for _, flag := range flags {
			assert.NotNil(t, leaf.Flags().Lookup(flag), path+" --"+flag)
		}
	}

	principalSpec := onlineIntentSpecs["principal create"]
	body, err := principalSpec.buildBody(map[string]string{"display-name": "Agent", "visibility": "requestable"}, nil, map[string]bool{"display-name": true, "visibility": true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"display_name":"Agent","visibility":"requestable"}`, string(body))
	grantSpec := onlineIntentSpecs["grant create"]
	body, err = grantSpec.buildBody(map[string]string{"name": "Test grant", "principal-id": id, "effect": "allow", "server-id": id}, nil, map[string]bool{"name": true, "principal-id": true, "effect": true, "server-id": true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"Test grant","principal_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","effect":"allow","server_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","upstream_name":null,"constraint":null,"expires_at":null}`, string(body))
}

type countingReader struct {
	reader *bytes.Buffer
	reads  int
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	reader.reads++
	return reader.reader.Read(buffer)
}
