package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// withChunkSize temporarily shrinks keychainChunkSize so tests force chunking
// without needing values larger than the backend's real limit.
func withChunkSize(t *testing.T, size int) {
	t.Helper()
	orig := keychainChunkSize
	keychainChunkSize = size
	t.Cleanup(func() { keychainChunkSize = orig })
}

func TestSplitChunks(t *testing.T) {
	require.Equal(t, []string{"abc"}, splitChunks("abc", 8))   // smaller than size
	require.Equal(t, []string{"abcd"}, splitChunks("abcd", 4)) // exactly size: no empty trailing
	require.Equal(t, []string{"ab", "cd", "e"}, splitChunks("abcde", 2))

	// Reassembly is byte-exact regardless of split point.
	s := strings.Repeat("hello world ", 50)
	require.Equal(t, s, strings.Join(splitChunks(s, 7), ""))
	for _, c := range splitChunks(s, 7) {
		require.LessOrEqual(t, len(c), 7)
		require.NotEmpty(t, c)
	}
}

func TestParseChunkManifest(t *testing.T) {
	n, ok := parseChunkManifest("go-keyring-chunked:3")
	require.True(t, ok)
	require.Equal(t, 3, n)

	_, ok = parseChunkManifest(`{"access_token":"x"}`)
	require.False(t, ok)
	_, ok = parseChunkManifest("go-keyring-chunked:abc")
	require.False(t, ok)
	_, ok = parseChunkManifest("go-keyring-chunked:-1")
	require.False(t, ok)
}

func TestWriteChunked_RoundTrip(t *testing.T) {
	withChunkSize(t, 8)
	value := strings.Repeat("payload-", 20) // 160 bytes -> 20 chunks at size 8

	require.NoError(t, writeChunked(keychainService, "rt-server", value))

	// Manifest at the primary key, chunks beneath it.
	manifest, err := keyring.Get(keychainService, "rt-server")
	require.NoError(t, err)
	n, ok := parseChunkManifest(manifest)
	require.True(t, ok)
	require.Equal(t, 20, n)

	got, err := keychainGet(keychainService, "rt-server")
	require.NoError(t, err)
	require.Equal(t, value, got)
}

func TestWriteChunked_ShrinkDeletesSurplusChunks(t *testing.T) {
	withChunkSize(t, 8)

	require.NoError(t, writeChunked(keychainService, "shrink-server", strings.Repeat("a", 80))) // 10 chunks
	require.NoError(t, writeChunked(keychainService, "shrink-server", strings.Repeat("b", 24))) // 3 chunks

	got, err := keychainGet(keychainService, "shrink-server")
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("b", 24), got)

	// Chunks 3..9 from the larger value must be gone.
	_, err = keyring.Get(keychainService, chunkKey("shrink-server", 3))
	require.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestKeychainSet_DirectWriteClearsStaleChunks(t *testing.T) {
	withChunkSize(t, 8)

	// Pre-existing chunked value, then a small value that fits in one item.
	require.NoError(t, writeChunked(keychainService, "stale-server", strings.Repeat("a", 80)))
	require.NoError(t, keychainSet(keychainService, "stale-server", `{"small":true}`))

	got, err := keychainGet(keychainService, "stale-server")
	require.NoError(t, err)
	require.Equal(t, `{"small":true}`, got)

	_, err = keyring.Get(keychainService, chunkKey("stale-server", 0))
	require.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestKeychainGet_MissingChunkErrors(t *testing.T) {
	// A manifest with no backing chunks should surface a read error rather than
	// silently returning a truncated value.
	require.NoError(t, keyring.Set(keychainService, "broken-server", "go-keyring-chunked:2"))

	_, err := keychainGet(keychainService, "broken-server")
	require.Error(t, err)
	require.Contains(t, err.Error(), "read chunk")
}

func TestKeychainTokenStore_GetToken_Chunked(t *testing.T) {
	withChunkSize(t, 16)
	token := &transport.Token{
		AccessToken:  strings.Repeat("jwt.", 50),
		TokenType:    "Bearer",
		RefreshToken: "refresh-xyz",
	}
	data, err := json.Marshal(token)
	require.NoError(t, err)
	require.NoError(t, writeChunked(keychainService, "chunked-token-server", string(data)))

	store := &KeychainTokenStore{serverName: "chunked-token-server"}
	got, err := store.GetToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, token.AccessToken, got.AccessToken)
	require.Equal(t, "refresh-xyz", got.RefreshToken)
}

func TestClearCredentials_RemovesChunks(t *testing.T) {
	withChunkSize(t, 8)
	require.NoError(t, writeChunked(keychainService, "chunked-clear-server", strings.Repeat("a", 64)))

	clearedToken, _, err := ClearCredentials("chunked-clear-server")
	require.NoError(t, err)
	require.True(t, clearedToken)

	store := &KeychainTokenStore{serverName: "chunked-clear-server"}
	_, err = store.GetToken(context.Background())
	require.ErrorIs(t, err, transport.ErrNoToken)

	_, err = keyring.Get(keychainService, chunkKey("chunked-clear-server", 0))
	require.ErrorIs(t, err, keyring.ErrNotFound)
}
