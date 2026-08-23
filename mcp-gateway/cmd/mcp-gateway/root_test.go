package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommandExposesOwnedOfflineCommands(t *testing.T) {
	cmd := newRootCmd()

	require.Equal(t, "mcp-gateway", cmd.Use)
	require.Contains(t, cmd.Short, "deny-by-default")
	require.Len(t, cmd.Commands(), 4)
	assert.Equal(t, []string{"admin-reset", "initialize", "restore", "serve"}, []string{
		cmd.Commands()[0].Name(), cmd.Commands()[1].Name(), cmd.Commands()[2].Name(), cmd.Commands()[3].Name(),
	})
	require.True(t, cmd.SilenceUsage)
	require.True(t, cmd.SilenceErrors)
}

func TestBaseSystemStatusIncludesS2OccupancyLimits(t *testing.T) {
	status := baseSystemStatus(
		"2026-08-22T00:00:00Z",
		storage.Identity{InstallationID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", SchemaVersion: 3},
		false,
		false,
		contract.KeyringReady,
		contract.LimitStatus{},
		contract.LimitStatus{},
		contract.LimitStatus{},
		contract.LimitStatus{},
		contract.LimitStatus{},
	)

	limits := map[string]contract.LimitStatus{
		"server_identities":       status.Limits.ServerIdentities,
		"servers":                 status.Limits.Servers,
		"downstream_runtimes":     status.Limits.DownstreamRuntimes,
		"server_reconciliations":  status.Limits.ServerReconciliations,
		"catalog_traversals":      status.Limits.CatalogTraversals,
		"oauth_flows":             status.Limits.OAuthFlows,
		"oauth_callback_work":     status.Limits.OAuthCallbackWork,
		"s2_idempotency_records":  status.Limits.S2IdempotencyRecords,
		"active_tools":            status.Limits.ActiveTools,
		"durable_tool_identities": status.Limits.DurableToolIdentities,
		"downstream_dispatch":     status.Limits.DownstreamDispatch,
	}
	for name, occupancy := range limits {
		fixed, ok := contract.FixedLimitByName(name)
		require.True(t, ok, name)
		require.Equal(t, contract.LimitStatus{Limit: fixed.Maximum}, occupancy, name)
	}
}

func TestInitializeAndResetEmitSafeResultsAndPublishSecretsOnce(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	initialSecret := filepath.Join(t.TempDir(), "initial-secret")
	stdout := new(bytes.Buffer)
	command := newRootCmd()
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"initialize", "--data-dir", root, "--secret-output", initialSecret})
	require.NoError(t, command.ExecuteContext(ctx))

	var initialized map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &initialized))
	assert.Equal(t, true, initialized["ok"])
	assert.Equal(t, "initialize", initialized["operation"])
	assert.Equal(t, "1", initialized["revision"])
	initialBearer, err := os.ReadFile(initialSecret)
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(initialBearer)))

	resetSecret := filepath.Join(t.TempDir(), "reset-secret")
	stdout.Reset()
	command = newRootCmd()
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"admin-reset", "--data-dir", root, "--secret-output", resetSecret})
	require.NoError(t, command.ExecuteContext(ctx))
	assert.JSONEq(t, `{"ok":true,"operation":"admin-reset","installation_id":"`+initialized["installation_id"].(string)+`","revision":"2"}`, stdout.String())
	resetBearer, err := os.ReadFile(resetSecret)
	require.NoError(t, err)
	assert.NotEqual(t, initialBearer, resetBearer)
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(resetBearer)))
}

func TestRestoreVerifyCurrentEmitsOneSafeMachineResult(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	stdout := new(bytes.Buffer)
	command := newRootCmd()
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"restore", "--verify-current", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(ctx))

	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, map[string]any{
		"ok":              true,
		"operation":       "restore",
		"mode":            "verify_current",
		"installation_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"revision":        "0",
	}, result)
}

func TestRestoreBackupEmitsSafeResultAndReplacementSecret(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	initialSecret := filepath.Join(t.TempDir(), "initial")
	command := newRootCmd()
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"initialize", "--data-dir", root, "--secret-output", initialSecret})
	require.NoError(t, command.ExecuteContext(ctx))

	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Open(ctx, ownership)
	require.NoError(t, err)
	manager, err := backup.New(backup.Options{Store: store, Layout: ownership.Layout(), Clock: systemClock{}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x55}, 128))})
	require.NoError(t, err)
	artifact, _, err := manager.Create(ctx, "authority", "restore-command")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	secret := filepath.Join(t.TempDir(), "replacement")
	stdout := new(bytes.Buffer)
	command = newRootCmd()
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"restore", artifact.ID, "--data-dir", root, "--secret-output", secret})
	require.NoError(t, command.ExecuteContext(ctx))
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, "backup", result["mode"])
	assert.Equal(t, artifact.ID, result["backup_id"])
	published, err := os.ReadFile(secret)
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(published)))
}

func TestRestoreRejectsInvalidInvocationWithSafeMachineResult(t *testing.T) {
	for _, args := range [][]string{
		{"restore"},
		{"restore", "unexpected"},
		{"restore", "--unknown-flag"},
	} {
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		command := newRootCmd()
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs(args)
		assert.Error(t, command.ExecuteContext(context.Background()))
		assert.JSONEq(t, `{"ok":false,"operation":"restore","code":"invalid_command"}`, stdout.String())
		assert.Empty(t, stderr.String())
	}
}

func TestRestoreVerifyCurrentFailureIsSafeAndNonzero(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newRootCmd()
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"restore", "--verify-current", "--data-dir", root})
	assert.Error(t, command.ExecuteContext(context.Background()))
	assert.JSONEq(t, `{"ok":false,"operation":"restore","code":"gateway_running"}`, stdout.String())
	assert.Empty(t, stderr.String())
}
