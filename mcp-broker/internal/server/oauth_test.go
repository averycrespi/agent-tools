package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func init() {
	keyring.MockInit()
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
	cfg := oauthConfig("github")

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

	cfg := oauthConfig("seeded-server")
	require.Equal(t, "stored-cid", cfg.ClientID)
	require.Equal(t, "stored-secret", cfg.ClientSecret)
}

func TestOAuthConfig_EmptyWhenNoStoredCreds(t *testing.T) {
	cfg := oauthConfig("no-creds-server")
	require.Empty(t, cfg.ClientID)
	require.Empty(t, cfg.ClientSecret)
}
