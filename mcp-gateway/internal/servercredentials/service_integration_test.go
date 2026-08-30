//go:build integration

package servercredentials

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	replacementInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	replacementServerID       = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
)

type replacementClock struct{}

func (replacementClock) Now() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }

type replacementEntropy struct{ value byte }

func (entropy *replacementEntropy) Read(buffer []byte) (int, error) {
	entropy.value++
	for index := range buffer {
		buffer[index] = entropy.value
	}
	return len(buffer), nil
}

type replacementCoordinator struct {
	store    *storage.Store
	entropy  *replacementEntropy
	captured []byte
}

func (coordinator *replacementCoordinator) ReplaceFenced(ctx context.Context, namespace keyring.Namespace, secret []byte, callback keyring.AuthorityCallback) (keyring.CutoverResult, error) {
	handle, err := keyring.NewHandle(coordinator.entropy)
	if err != nil {
		return keyring.CutoverResult{}, err
	}
	coordinator.captured = append([]byte(nil), secret...)
	var revision string
	err = coordinator.store.Mutate(ctx, func(transaction *sql.Tx) error {
		var callbackErr error
		revision, callbackErr = callback(ctx, transaction, keyring.AuthorityUpdate{Owner: namespace.Owner(), Kind: namespace.Kind(), Handle: &handle})
		return callbackErr
	})
	return keyring.CutoverResult{Handle: handle, Revision: revision}, err
}

func TestServicePublishesOneCompleteStaticGenerationAndSafeOperation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()
	store, err := storage.Initialize(context.Background(), ownership, replacementInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	entropy := new(replacementEntropy)
	repository, err := servers.New(store, replacementClock{}, entropy)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte("replacement"))
	created, err := repository.Create(context.Background(), servers.CreateRequest{ID: replacementServerID, Definition: servers.Definition{
		Namespace: "replacement", DisplayName: "Replacement", Enabled: false,
		Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "token"}},
	}, Idempotency: &servers.IdempotencyRequest{AuthorityID: replacementInstallationID, Method: "POST", Route: "/api/v1/servers", Key: "replacement", RequestHash: digest}})
	require.NoError(t, err)
	coordinator := &replacementCoordinator{store: store, entropy: entropy}
	fenced := 0
	service, err := New(repository, coordinator, replacementInstallationID, func(string) { fenced++ }, nil)
	require.NoError(t, err)
	plan, err := service.Prepare(context.Background(), servers.CredentialReplacementRequest{ServerID: created.Server.ID, Kind: contract.ServerCredentialStatic, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0", Slots: []string{"token"}})
	require.NoError(t, err)
	secret, err := EncodeStaticGeneration(map[string]string{"token": "replacement-canary"})
	require.NoError(t, err)
	publication, err := service.Replace(context.Background(), plan, secret)
	require.NoError(t, err)
	assert.Equal(t, 1, fenced)
	assert.Equal(t, "1", publication.Revision)
	assert.Equal(t, contract.OperationCredentialReplace, publication.Operation.Kind)
	assert.Equal(t, "1", publication.Operation.TargetCredentialRevisions.StaticCredential)
	assert.Equal(t, make([]byte, len(secret)), secret)
	generation, err := DecodeStaticGeneration(coordinator.captured)
	require.NoError(t, err)
	assert.Equal(t, "replacement-canary", generation.Values["token"])
	stored, err := repository.Get(context.Background(), created.Server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", stored.DesiredRevision)
	backup := filepath.Join(t.TempDir(), "replacement-safe.db")
	require.NoError(t, store.BackupTo(context.Background(), backup))
	contents, err := os.ReadFile(backup)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(contents, []byte("replacement-canary")))
}
