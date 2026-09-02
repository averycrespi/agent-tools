package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIValidatedItemETagLoader(t *testing.T) {
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	serverBody := mustJSON(t, serverWire{ID: id, Namespace: "example", DisplayName: "Example", DesiredState: contract.DesiredServerEnabled, DesiredRevision: "7"})
	principalBody := mustJSON(t, contract.Principal{ID: id, DisplayName: "Agent", State: contract.PrincipalActive, Visibility: contract.VisibilityRequestable, Revision: "7", CredentialRevision: "1"})
	requestBody := mustJSON(t, contract.GrantRequest{GrantRequestSummary: contract.GrantRequestSummary{ID: id, PrincipalID: id, State: contract.RequestPending, Revision: "7"}, ResolvedServerID: id})

	valid := []struct {
		name string
		kind onlineItemKind
		body []byte
		etag string
	}{
		{name: "server", kind: onlineItemServer, body: serverBody, etag: contract.ServerETag(id, "7")},
		{name: "principal", kind: onlineItemPrincipal, body: principalBody, etag: contract.PrincipalETag(id, "7")},
		{name: "grant request", kind: onlineItemGrantRequest, body: requestBody, etag: contract.GrantRequestETag(id, "7")},
	}
	for _, test := range valid {
		t.Run("valid/"+test.name, func(t *testing.T) {
			server := itemResponseServer(test.body, controlclient.MediaTypeJSON, test.etag)
			defer server.Close()
			loaded, failure := loadValidatedItem(itemCommand(), itemOptions(server.URL), test.kind, id, controlclient.RequestPhaseRead)
			require.Nil(t, failure)
			assert.Equal(t, test.etag, loaded.ETag)
			assert.Equal(t, test.body, loaded.Body)
		})
	}

	invalid := []struct {
		name        string
		body        []byte
		contentType string
		etag        string
	}{
		{name: "missing etag", body: serverBody, contentType: controlclient.MediaTypeJSON},
		{name: "weak etag", body: serverBody, contentType: controlclient.MediaTypeJSON, etag: `W/"server-` + id + `-7"`},
		{name: "malformed etag", body: serverBody, contentType: controlclient.MediaTypeJSON, etag: `"server"`},
		{name: "wrong resource etag", body: serverBody, contentType: controlclient.MediaTypeJSON, etag: contract.PrincipalETag(id, "7")},
		{name: "wrong identity", body: mustJSON(t, serverWire{ID: "01BX5ZZKBKACTAV9WEVGEMMVRZ", DesiredRevision: "7"}), contentType: controlclient.MediaTypeJSON, etag: contract.ServerETag(id, "7")},
		{name: "wrong revision", body: serverBody, contentType: controlclient.MediaTypeJSON, etag: contract.ServerETag(id, "8")},
		{name: "wrong body", body: principalBody, contentType: controlclient.MediaTypeJSON, etag: contract.ServerETag(id, "7")},
		{name: "wrong media", body: serverBody, contentType: "text/plain", etag: contract.ServerETag(id, "7")},
	}
	for _, test := range invalid {
		t.Run("invalid/"+test.name, func(t *testing.T) {
			server := itemResponseServer(test.body, test.contentType, test.etag)
			defer server.Close()
			_, failure := loadValidatedItem(itemCommand(), itemOptions(server.URL), onlineItemServer, id, controlclient.RequestPhasePreflight)
			require.NotNil(t, failure)
			assert.Equal(t, "client_response_invalid", failure.Code)
			assert.False(t, failure.Uncertain)
		})
	}

	t.Run("truncated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", controlclient.MediaTypeJSON)
			response.Header().Set("ETag", contract.ServerETag(id, "7"))
			_, _ = response.Write([]byte(`{"id":`))
		}))
		defer server.Close()
		_, failure := loadValidatedItem(itemCommand(), itemOptions(server.URL), onlineItemServer, id, controlclient.RequestPhasePreflight)
		require.NotNil(t, failure)
		assert.Equal(t, "client_response_invalid", failure.Code)
		assert.False(t, failure.Uncertain)
	})

	t.Run("human includes etag and json remains exact", func(t *testing.T) {
		server := itemResponseServer(serverBody, controlclient.MediaTypeJSON, contract.ServerETag(id, "7"))
		defer server.Close()
		command := itemCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		options := itemOptions(server.URL)
		options.output = string(controlclient.OutputHuman)
		require.NoError(t, runOnlineItemRead(command, options, onlineItemServer, id, serverItemTable))
		assert.Contains(t, output.String(), "ETAG")
		assert.Contains(t, output.String(), contract.ServerETag(id, "7"))

		output.Reset()
		options.output = string(controlclient.OutputJSON)
		require.NoError(t, runOnlineItemRead(command, options, onlineItemServer, id, serverItemTable))
		assert.Equal(t, string(serverBody)+"\n", output.String())
	})
}

func itemCommand() *cobra.Command {
	command := &cobra.Command{}
	command.SetContext(context.Background())
	return command
}

func itemOptions(rawURL string) *onlineOptions {
	return &onlineOptions{address: strings.Replace(rawURL, "http://[::1]", "http://127.0.0.1", 1), adminBearer: onlineAdminBearer{value: testAdministratorBearer}}
}

func itemResponseServer(body []byte, contentType, etag string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", contentType)
		if etag != "" {
			response.Header().Set("ETag", etag)
		}
		_, _ = response.Write(body)
	}))
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}
