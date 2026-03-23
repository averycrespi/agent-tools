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
