package server

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/zalando/go-keyring"
)

const keychainService = "mcp-broker"

// KeychainTokenStore implements transport.TokenStore using the OS keychain.
type KeychainTokenStore struct {
	serverName string
}

func (s *KeychainTokenStore) GetToken(ctx context.Context) (*transport.Token, error) {
	data, err := keyring.Get(keychainService, s.serverName)
	if err == keyring.ErrNotFound {
		return nil, transport.ErrNoToken
	}
	if err != nil {
		return nil, fmt.Errorf("keychain get: %w", err)
	}
	var token transport.Token
	if err := json.Unmarshal([]byte(data), &token); err != nil {
		return nil, fmt.Errorf("unmarshal token: %w", err)
	}
	return &token, nil
}

func (s *KeychainTokenStore) SaveToken(ctx context.Context, token *transport.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	return keyring.Set(keychainService, s.serverName, string(data))
}

// callbackPort returns a deterministic port for the OAuth callback server,
// derived from the server name. Maps to ephemeral range 10000-65535.
func callbackPort(serverName string) int {
	h := fnv.New32a()
	h.Write([]byte(serverName))
	return int(h.Sum32()%(65535-10000)) + 10000
}
