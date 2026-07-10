package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func init() {
	keyring.MockInit()
}

func TestOAuthFlowContextHasDedicatedTimeout(t *testing.T) {
	ctx, cancel := oauthFlowContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), deadline, time.Second)
}

func TestKeychainTokenStore_SaveAndGet(t *testing.T) {
	store := &KeychainTokenStore{serverName: "test-server"}
	ctx := context.Background()

	token := &transport.Token{
		AccessToken:  "access-123",
		TokenType:    "Bearer",
		RefreshToken: "refresh-456",
	}

	err := store.SaveToken(ctx, token)
	require.NoError(t, err)

	got, err := store.GetToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "access-123", got.AccessToken)
	require.Equal(t, "Bearer", got.TokenType)
	require.Equal(t, "refresh-456", got.RefreshToken)
}

func TestKeychainTokenStore_GetToken_NoToken(t *testing.T) {
	store := &KeychainTokenStore{serverName: "nonexistent-server"}
	ctx := context.Background()

	_, err := store.GetToken(ctx)
	require.ErrorIs(t, err, transport.ErrNoToken)
}

func TestKeychainTokenStore_GetToken_CorruptedToken(t *testing.T) {
	// If the keychain contains invalid JSON, GetToken should return an unmarshal error.
	store := &KeychainTokenStore{serverName: "corrupted-server"}
	ctx := context.Background()

	err := keyring.Set(keychainService, "corrupted-server", "not-valid-json")
	require.NoError(t, err)

	_, err = store.GetToken(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unmarshal token")
}

func TestCallbackPort_Deterministic(t *testing.T) {
	port1 := callbackPort("github")
	port2 := callbackPort("github")
	require.Equal(t, port1, port2)

	require.GreaterOrEqual(t, port1, 10000)
	require.LessOrEqual(t, port1, 65535)
}

func TestCallbackPort_DifferentServers(t *testing.T) {
	portGH := callbackPort("github")
	portAT := callbackPort("atlassian")
	require.NotEqual(t, portGH, portAT)
}

func TestOAuthConfig_RedirectURIMatchesCallbackPort(t *testing.T) {
	cfg := oauthConfig("github", config.ServerConfig{})

	port := callbackPort("github")
	expected := fmt.Sprintf("http://localhost:%d/callback", port)
	require.Equal(t, expected, cfg.RedirectURI)
	require.True(t, cfg.PKCEEnabled)
	require.NotNil(t, cfg.TokenStore)
}

func TestClientCreds_SaveAndGet(t *testing.T) {
	err := saveClientCreds("test-server", clientCreds{
		ClientID:     "cid-123",
		ClientSecret: "csecret-456",
	})
	require.NoError(t, err)

	got, err := getClientCreds("test-server")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "cid-123", got.ClientID)
	require.Equal(t, "csecret-456", got.ClientSecret)
}

func TestClientCreds_GetNoCreds(t *testing.T) {
	got, err := getClientCreds("unregistered-server")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestOAuthConfig_SeedsFromStoredCreds(t *testing.T) {
	err := saveClientCreds("seeded-server", clientCreds{
		ClientID:     "stored-cid",
		ClientSecret: "stored-secret",
	})
	require.NoError(t, err)

	cfg := oauthConfig("seeded-server", config.ServerConfig{})
	require.Equal(t, "stored-cid", cfg.ClientID)
	require.Equal(t, "stored-secret", cfg.ClientSecret)
}

func TestOAuthConfig_EmptyWhenNoStoredCreds(t *testing.T) {
	cfg := oauthConfig("no-creds-server", config.ServerConfig{})
	require.Empty(t, cfg.ClientID)
	require.Empty(t, cfg.ClientSecret)
}

func TestClientCreds_GetCorruptedCreds(t *testing.T) {
	// If the keychain contains invalid JSON, getClientCreds should return an unmarshal error.
	err := keyring.Set(keychainService, "corrupted-creds-server.client", "not-valid-json")
	require.NoError(t, err)

	_, err = getClientCreds("corrupted-creds-server")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unmarshal client creds")
}

func TestClearCredentials_RemovesTokenAndClient(t *testing.T) {
	ctx := context.Background()
	store := &KeychainTokenStore{serverName: "logout-server"}
	require.NoError(t, store.SaveToken(ctx, &transport.Token{AccessToken: "a", TokenType: "Bearer"}))
	require.NoError(t, saveClientCreds("logout-server", clientCreds{ClientID: "cid", ClientSecret: "csecret"}))

	clearedToken, clearedClient, err := ClearCredentials("logout-server")
	require.NoError(t, err)
	require.True(t, clearedToken)
	require.True(t, clearedClient)

	_, err = store.GetToken(ctx)
	require.ErrorIs(t, err, transport.ErrNoToken)
	got, err := getClientCreds("logout-server")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestClearCredentials_MissingEntriesAreNotAnError(t *testing.T) {
	clearedToken, clearedClient, err := ClearCredentials("never-logged-in-server")
	require.NoError(t, err)
	require.False(t, clearedToken)
	require.False(t, clearedClient)
}

func TestClearCredentials_TokenOnly(t *testing.T) {
	ctx := context.Background()
	store := &KeychainTokenStore{serverName: "token-only-server"}
	require.NoError(t, store.SaveToken(ctx, &transport.Token{AccessToken: "a", TokenType: "Bearer"}))

	clearedToken, clearedClient, err := ClearCredentials("token-only-server")
	require.NoError(t, err)
	require.True(t, clearedToken)
	require.False(t, clearedClient)
}

func TestOAuthConfig_KeychainErrorContinuesWithEmptyCreds(t *testing.T) {
	// If the keychain contains invalid JSON for the client entry, oauthConfig
	// should log to stderr and return a config with empty ClientID/ClientSecret
	// (graceful degradation: mcp-go will re-register rather than failing).
	err := keyring.Set(keychainService, "keychain-error-server.client", "not-valid-json")
	require.NoError(t, err)

	cfg := oauthConfig("keychain-error-server", config.ServerConfig{})
	require.Empty(t, cfg.ClientID)
	require.Empty(t, cfg.ClientSecret)
}

func TestOAuthConfig_UsesConfiguredClientAndCallbackPort(t *testing.T) {
	cfg := oauthConfig("remote", config.ServerConfig{
		OAuth: &config.OAuthConfig{
			ClientID:     "test-client-id",
			CallbackPort: 3118,
		},
	})

	require.Equal(t, "test-client-id", cfg.ClientID)
	require.Equal(t, "http://localhost:3118/callback", cfg.RedirectURI)
	require.True(t, cfg.PKCEEnabled)
	require.NotNil(t, cfg.TokenStore)
}

func TestOAuthConfig_ConfiguredClientOverridesStoredCreds(t *testing.T) {
	require.NoError(t, saveClientCreds("configured-server", clientCreds{ClientID: "stored-cid", ClientSecret: "stored-secret"}))

	cfg := oauthConfig("configured-server", config.ServerConfig{
		OAuth: &config.OAuthConfig{ClientID: "configured-cid"},
	})

	require.Equal(t, "configured-cid", cfg.ClientID)
	require.Empty(t, cfg.ClientSecret)
}

func TestOAuthConfig_ExpandsConfiguredClientSecret(t *testing.T) {
	t.Setenv("OAUTH_CLIENT_SECRET", "expanded-secret")

	cfg := oauthConfig("secret-server", config.ServerConfig{
		OAuth: &config.OAuthConfig{ClientID: "cid", ClientSecret: "$OAUTH_CLIENT_SECRET"},
	})

	require.Equal(t, "cid", cfg.ClientID)
	require.Equal(t, "expanded-secret", cfg.ClientSecret)
}

func TestOAuthConfig_UsesConfiguredAuthServerMetadataURL(t *testing.T) {
	cfg := oauthConfig("metadata-server", config.ServerConfig{
		OAuth: &config.OAuthConfig{
			ClientID:              "cid",
			AuthServerMetadataURL: "https://example.com/.well-known/oauth-authorization-server",
		},
	})

	require.Equal(t, "https://example.com/.well-known/oauth-authorization-server", cfg.AuthServerMetadataURL)
}
