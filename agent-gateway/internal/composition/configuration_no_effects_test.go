package composition

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestOnlineConfigurationPreviewAndMissingAuthorityNeverAcquireMaterial(t *testing.T) {
	ctx := audit.WithSystem(t.Context())
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	options.Entropy = rand.Reader
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	require.NoError(t, built.Start(ctx))
	var requests atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	sentinel := filepath.Join(t.TempDir(), "launched")
	executable := filepath.Join(t.TempDir(), "server")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nprintf launched > '"+sentinel+"'\n"), 0700))
	definitions := []contract.ConfigDefinition{}
	add := func(key string, enabled bool, transport contract.Transport, credentials contract.ConfigServerCredentials) {
		raw, err := json.Marshal(contract.ConfigServerSpec{DisplayName: key, Enabled: enabled, Transport: mustMarshalConfiguration(t, transport), Credentials: credentials})
		require.NoError(t, err)
		definitions = append(definitions, contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: "mcp_server", Key: key, Spec: raw})
	}
	add("stdio", true, contract.StdioTransport{Kind: contract.TransportStdio, Executable: executable, Arguments: []string{}, WorkingDirectory: filepath.Dir(executable), Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "primary"}}, contract.ConfigServerCredentials{Static: map[string]string{"primary": "mcp/stdio/static/primary"}})
	add("bearer", true, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: upstream.URL + "/mcp", ProtocolMode: contract.ProtocolAuto, AllowPrivateHTTP: true, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}}, contract.ConfigServerCredentials{Static: map[string]string{"bearer": "mcp/bearer/static/bearer"}})
	issuer := upstream.URL
	add("oauth", true, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: upstream.URL + "/mcp", ProtocolMode: contract.ProtocolAuto, AllowPrivateHTTP: true, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, Issuer: &issuer, ClientID: "authored-client", TokenEndpointAuthMethod: contract.TokenEndpointAuthNone}, TrustedOrigins: []string{}}}, contract.ConfigServerCredentials{Static: map[string]string{}, OAuthAuthorization: true})
	add("disabled", false, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: upstream.URL + "/mcp", ProtocolMode: contract.ProtocolAuto, AllowPrivateHTTP: true, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}}, contract.ConfigServerCredentials{Static: map[string]string{}})
	raw := mustMarshalConfiguration(t, contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: definitions})
	before, err := options.Store.Identity(ctx)
	require.NoError(t, err)
	preview, err := built.configurationImport.Preview(ctx, raw)
	require.NoError(t, err)
	require.Len(t, preview.Creations, 4)
	after, err := options.Store.Identity(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Zero(t, requests.Load())
	_, err = os.Stat(sentinel)
	require.ErrorIs(t, err, os.ErrNotExist)
	result, err := built.configurationImport.Import(ctx, raw)
	require.NoError(t, err)
	require.Equal(t, "committed", result.Configuration)
	require.Eventually(t, func() bool {
		owned, waiting := options.Store.MutationOccupancy()
		if owned || waiting != 0 || built.ReconciliationStatus().InUse != 0 {
			return false
		}
		for _, created := range result.Creations {
			status := built.RuntimeStatus(created.ID)
			if created.Key != "disabled" && status.CredentialState == contract.ServerCredentialState("") {
				return false
			}
		}
		return true
	}, 5*time.Second, time.Millisecond)
	require.Zero(t, requests.Load())
	_, err = os.Stat(sentinel)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, options.Store.View(ctx, func(tx *sql.Tx) error {
		var authority int
		err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM secret_generations)+(SELECT count(*) FROM server_auth_flows)+(SELECT count(*) FROM server_oauth_registrations WHERE revision<>0)+(SELECT count(*) FROM server_import_activation)`).Scan(&authority)
		require.Zero(t, authority)
		return err
	}))
	for _, created := range result.Creations {
		resource, err := built.servers.Get(ctx, created.ID)
		require.NoError(t, err)
		require.False(t, resource.ActivationRequired)
		if created.Key != "disabled" {
			require.Equal(t, contract.DesiredServerEnabled, resource.DesiredState)
			require.NotEqual(t, contract.RuntimeActive, built.RuntimeStatus(created.ID).State)
		}
	}
	require.Zero(t, (<-built.Drain(ctx)).Unconfirmed)
}
func mustMarshalConfiguration(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}
