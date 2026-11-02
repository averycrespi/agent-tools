package composition

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestConfigurationImportAndResumeNeverContactUpstreamsOrAuthorizeOAuth(t *testing.T) {
	var connections atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	upstream.StartTLS()
	defer upstream.Close()
	value := contract.ConfigExport{Format: contract.ConfigExportFormat, Version: 1, Agents: []contract.ConfigAgent{}, MCP: contract.ConfigMCP{Servers: []contract.ConfigServer{}, Grants: []contract.ConfigMCPGrant{}}, HTTP: contract.ConfigHTTP{Credentials: []contract.ConfigHTTPCredential{}, Grants: []contract.ConfigHTTPGrant{}}, Git: contract.ConfigGit{Credentials: []contract.ConfigGitCredential{}, Repositories: []contract.ConfigGitRepository{}, Grants: []contract.ConfigGitGrant{}, RoutingOrigins: []string{}}}
	for _, kind := range []string{"none", "bearer", "oauth"} {
		var authentication contract.HTTPAuthentication = contract.NoAuthentication{Mode: contract.AuthenticationNone}
		requirements := contract.ConfigServerCredentials{Static: map[string]string{}}
		switch kind {
		case "bearer":
			authentication = contract.BearerAuthentication{Mode: contract.AuthenticationBearer}
			requirements.Static["bearer"] = "mcp/bearer/static/bearer"
		case "oauth":
			authentication = contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}, TrustedOrigins: []string{}}
			requirements.OAuthAuthorization = true
		}
		transport, err := json.Marshal(contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: upstream.URL + "/mcp", ProtocolMode: contract.ProtocolModern, Authentication: authentication})
		require.NoError(t, err)
		value.MCP.Servers = append(value.MCP.Servers, contract.ConfigServer{Ref: "mcp/" + kind, Namespace: kind, DisplayName: kind, Enabled: true, Transport: transport, Credentials: requirements})
	}
	data, err := json.Marshal(value)
	require.NoError(t, err)
	owner, plan := importBytesFixture(t, data)
	clock := testutil.NewFakeClock(compositionTime)
	_, err = initializeOwned(t.Context(), owner, plan, clock, rand.Reader, func(point string) error {
		if point == "import-transaction-committed" {
			return errors.New("interrupted")
		}
		return nil
	})
	require.Error(t, err)
	require.Zero(t, connections.Load())
	_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
	require.NoError(t, err)
	require.Zero(t, connections.Load())
	store, err := storage.Open(t.Context(), owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	built, err := New(Options{Ownership: owner, Store: store, InstallationID: plan.Installation, CallbackURL: "http://127.0.0.1:47103/oauth/callback", Clock: clock, Entropy: rand.Reader, Invalidate: func(contract.Invalidation) {}, Ready: func() bool { return true }})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	require.NoError(t, built.Start(t.Context()))
	require.Zero(t, connections.Load(), "credential-free and OAuth upstreams remain fenced even on first serve")
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		var count int
		for _, table := range []string{"server_operations", "server_auth_flows"} {
			require.NoError(t, tx.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
			require.Zero(t, count, table)
		}
		require.NoError(t, tx.QueryRow(`SELECT count(*) FROM server_import_activation`).Scan(&count))
		require.Equal(t, 3, count)
		return nil
	}))
	require.Zero(t, (<-built.Drain(t.Context())).Unconfirmed)
}
