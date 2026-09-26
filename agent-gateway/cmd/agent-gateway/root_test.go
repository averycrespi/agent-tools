package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIServePostStartFailureOutput(t *testing.T) {
	for _, mode := range []controlclient.OutputMode{controlclient.OutputHuman, controlclient.OutputJSON} {
		t.Run(string(mode), func(t *testing.T) {
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			renderer, err := controlclient.NewRenderer(mode, stdout, stderr)
			require.NoError(t, err)
			phases := controlclient.NewServePhases(renderer)
			require.NoError(t, phases.Acknowledge([]byte(`{"ok":true,"operation":"serve"}`), "Gateway started successfully."))
			startup := stdout.String()
			problem := serveCommandProblem(context.DeadlineExceeded, phases.Acknowledged(), "/private-installation")
			require.NoError(t, phases.WriteProblem(problem))
			assert.Equal(t, 7, commandExitCode(problem))
			assert.Equal(t, startup, stdout.String(), "post-start failure must not emit another startup or write to stdout")
			assert.ErrorIs(t, phases.Acknowledge([]byte(`{"ok":true}`), "duplicate startup"), controlclient.ErrInvalidInput)
			assert.NotContains(t, stderr.String(), "/private-installation")
			assert.Contains(t, stderr.String(), "clean shutdown could not be confirmed")
			assert.Contains(t, stderr.String(), "marked unclean")
			if mode == controlclient.OutputJSON {
				assert.JSONEq(t, `{"ok":true,"operation":"serve"}`, startup)
				var rendered controlclient.Problem
				require.NoError(t, json.Unmarshal(stderr.Bytes(), &rendered))
				assert.Equal(t, "serve_stopped", rendered.Code)
				assert.Equal(t, 7, rendered.Exit)
			} else {
				assert.Equal(t, "Gateway started successfully.\n", startup)
			}
		})
	}
}

func TestCLIExecutionOptionsResolveOnce(t *testing.T) {
	tests := []struct {
		name      string
		input     executionOptionInput
		wantMode  controlclient.OutputMode
		wantError bool
	}{
		{name: "default human", input: executionOptionInput{DataDir: "/data"}, wantMode: controlclient.OutputHuman},
		{name: "explicit human", input: executionOptionInput{Output: "human", OutputSet: true}, wantMode: controlclient.OutputHuman},
		{name: "table compatibility", input: executionOptionInput{Output: "table", OutputSet: true}, wantMode: controlclient.OutputHuman},
		{name: "explicit json", input: executionOptionInput{Output: "json", OutputSet: true}, wantMode: controlclient.OutputJSON},
		{name: "json shorthand", input: executionOptionInput{JSON: true}, wantMode: controlclient.OutputJSON},
		{name: "matching selectors", input: executionOptionInput{Output: "json", OutputSet: true, JSON: true}, wantMode: controlclient.OutputJSON},
		{name: "conflicting selectors", input: executionOptionInput{Output: "human", OutputSet: true, JSON: true}, wantError: true},
		{name: "invalid output", input: executionOptionInput{Output: "yaml", OutputSet: true}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := resolveExecutionOptions(test.input)
			if test.wantError {
				assert.ErrorIs(t, err, controlclient.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.input.DataDir, resolved.DataDir)
			assert.Equal(t, test.wantMode, resolved.Output)
		})
	}
}

func TestRootCommandExposesOwnedOfflineCommands(t *testing.T) {
	cmd := newTestRootCmd(t)

	require.Equal(t, "agent-gateway", cmd.Use)
	require.Contains(t, cmd.Short, "deny-by-default")
	for _, path := range [][]string{{"maintenance", "reset-admin-credentials"}, {"init"}, {"maintenance", "restore-backup"}, {"maintenance", "verify-and-recover-storage"}, {"doctor"}, {"serve"}} {
		command, _, err := cmd.Find(path)
		require.NoError(t, err)
		assert.Equal(t, path[len(path)-1], command.Name())
	}
	require.True(t, cmd.SilenceUsage)
	require.True(t, cmd.SilenceErrors)
}

func TestServeUsesResolvedDefaultAndLeavesPreStartStdoutEmpty(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"serve", "--json"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 4, commandExitCode(err))
	assert.Empty(t, stdout.String())
	lines := bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte{'\n'})
	require.Len(t, lines, 2)
	var problem controlclient.Problem
	require.NoError(t, json.Unmarshal(lines[1], &problem))
	assert.Equal(t, "not_initialized", problem.Code)
	assert.Contains(t, problem.Title, "agent-gateway init")
	_, statErr := os.Lstat(filepath.Join(xdg, gatewaypaths.InstallationName))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestInitializeResumesInterruptedControlCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	secret := filepath.Join(t.TempDir(), "secret")
	first := newRootCmdWithDependencies(offlineDependencies{clock: systemClock{}, entropy: bytes.NewReader(make([]byte, 10)), newComposition: composition.New})
	first.SetOut(new(bytes.Buffer))
	first.SetErr(new(bytes.Buffer))
	first.SetArgs([]string{"init", "--confirm", "--data-dir", root, "--secret-output", secret})
	require.Error(t, first.ExecuteContext(t.Context()), "entropy ends after control creation, before traffic generation")
	_, err := os.Stat(filepath.Join(root, gatewaypaths.DatabaseName))
	require.NoError(t, err)
	_, err = os.Stat(secret)
	require.ErrorIs(t, err, os.ErrNotExist, "no administrator authority published for incomplete pair")
	retry := newTestRootCmd(t)
	retry.SetOut(new(bytes.Buffer))
	retry.SetErr(new(bytes.Buffer))
	retry.SetArgs([]string{"init", "--confirm", "--data-dir", root, "--secret-output", secret})
	require.NoError(t, retry.ExecuteContext(t.Context()))
	_, err = composition.VerifyStorage(t.Context(), root)
	require.NoError(t, err, "retry publishes authority only with a fully verified selected pair")
	_, err = os.Stat(secret)
	require.NoError(t, err)
}

func TestInitializePersistentDataDirRendersMatchingServeCommand(t *testing.T) {
	root := filepath.Join(t.TempDir(), "custom gateway")
	stdout := new(bytes.Buffer)
	command := newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"--data-dir", root, "init", "--confirm"})
	require.NoError(t, command.ExecuteContext(context.Background()))
	assert.Contains(t, stdout.String(), "data_dir=$(printf '%b_' '"+root+"')")
	assert.Contains(t, stdout.String(), "agent-gateway serve --data-dir \"$data_dir\"")
}

func TestRootCompositionFailurePreventsStartupOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	initialize := newTestRootCmd(t)
	initialize.SetOut(new(bytes.Buffer))
	initialize.SetErr(new(bytes.Buffer))
	initialize.SetArgs([]string{"init", "--confirm", "--data-dir", root, "--secret-output", filepath.Join(t.TempDir(), "secret")})
	require.NoError(t, initialize.ExecuteContext(context.Background()))

	stdout := new(bytes.Buffer)
	command := newRootCmdWithDependencies(offlineDependencies{
		clock:   systemClock{},
		entropy: bytes.NewReader(bytes.Repeat([]byte{0x55}, 1024)),
		newComposition: func(composition.Options) (*composition.Composition, error) {
			return nil, errors.New("injected composition failure")
		},
	})
	command.SetOut(stdout)
	stderr := new(bytes.Buffer)
	command.SetErr(stderr)
	command.SetArgs([]string{"serve", "--data-dir", root, "--listen", "127.0.0.1:8210", "--json"})

	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 7, commandExitCode(err))
	assert.Empty(t, stdout.String())
	lines := bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte{'\n'})
	require.Len(t, lines, 2)
	var diagnostic struct {
		Event string `json:"event"`
	}
	require.NoError(t, json.Unmarshal(lines[0], &diagnostic))
	require.Equal(t, "lifecycle_failure", diagnostic.Event)
	var problem controlclient.Problem
	require.NoError(t, json.Unmarshal(lines[1], &problem))
	assert.Equal(t, "storage_unavailable", problem.Code)
}

func TestStatusBaseIncludesGlobalRequestLimitsAndPositiveAgentAuth(t *testing.T) {
	status := baseSystemStatus(
		"2026-08-22T00:00:00Z",
		storage.Identity{InstallationID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", SchemaVersion: 3},
		false,
		false,
		contract.AgentAuthPrincipalCredentials,
		contract.KeyringReady,
		contract.LimitStatus{},
		contract.LimitStatus{},
		contract.LimitStatus{},
		contract.LimitStatus{},
		contract.LimitStatus{},
	)

	limits := map[string]contract.LimitStatus{
		"server_identities":            status.Limits.ServerIdentities,
		"servers":                      status.Limits.Servers,
		"downstream_runtimes":          status.Limits.DownstreamRuntimes,
		"server_reconciliations":       status.Limits.ServerReconciliations,
		"catalog_traversals":           status.Limits.CatalogTraversals,
		"oauth_flows":                  status.Limits.OAuthFlows,
		"oauth_callback_work":          status.Limits.OAuthCallbackWork,
		"server_idempotency_records":   status.Limits.ServerIdempotencyRecords,
		"active_tools":                 status.Limits.ActiveTools,
		"durable_tool_identities":      status.Limits.DurableToolIdentities,
		"downstream_dispatch":          status.Limits.DownstreamDispatch,
		"principals":                   status.Limits.Principals,
		"grants":                       status.Limits.Grants,
		"grant_requests":               status.Limits.GrantRequests,
		"grant_request_evidence_bytes": status.Limits.GrantRequestEvidenceBytes,
	}
	for name, occupancy := range limits {
		fixed, ok := contract.FixedLimitByName(name)
		require.True(t, ok, name)
		require.Equal(t, contract.LimitStatus{Limit: fixed.Maximum}, occupancy, name)
	}
	assert.Equal(t, contract.AgentAuthPrincipalCredentials, status.Protocols.AgentAuth)
}

func TestInitializeDefaultsToXDGWithHumanNextSteps(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"init", "--confirm"})
	require.NoError(t, command.ExecuteContext(context.Background()))

	root := filepath.Join(xdg, gatewaypaths.InstallationName)
	bearerPath := filepath.Join(root, gatewaypaths.AdminBearerName)
	bearer, err := os.ReadFile(bearerPath)
	require.NoError(t, err)
	info, err := os.Lstat(bearerPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Contains(t, stdout.String(), "Gateway setup is complete.")
	assert.Contains(t, stdout.String(), root)
	assert.Contains(t, stdout.String(), bearerPath)
	assert.Contains(t, stdout.String(), "agent-gateway serve")
	assert.Contains(t, stdout.String(), "Public certificate:")
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(bearer)))
	assert.Contains(t, stderr.String(), "Proposed changes:")

	stdout.Reset()
	stderr.Reset()
	command = newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"init", "--confirm"})
	err = command.ExecuteContext(context.Background())
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "setup is complete")
	assert.Empty(t, stderr.String())
	unchanged, readErr := os.ReadFile(bearerPath)
	require.NoError(t, readErr)
	assert.Equal(t, bearer, unchanged)
}

func TestInitializeRejectsConflictingOutputBeforeMutation(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"init", "--confirm", "--json", "--output", "human"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "flag is not recognized")
	_, statErr := os.Lstat(filepath.Join(xdg, gatewaypaths.InstallationName))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestInitializeRendersShellSafeCustomStartCommand(t *testing.T) {
	root := filepath.Join(t.TempDir(), "space ' quote\ncontrol", "gateway")
	secret := filepath.Join(t.TempDir(), "secret")
	stdout := new(bytes.Buffer)
	command := newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--confirm", "--data-dir", root, "--secret-output", secret})
	require.NoError(t, command.ExecuteContext(context.Background()))
	assert.NotContains(t, stdout.String(), "quote\ncontrol")
	assert.Contains(t, stdout.String(), "printf '%b_'")
	assert.Contains(t, stdout.String(), `\012`)
	assert.Contains(t, stdout.String(), `'"'"'`)
	assert.Contains(t, stdout.String(), "cannot be shown again")
	assert.Contains(t, stdout.String(), "agent-gateway serve --data-dir")
}

func TestInitializeAndResetEmitSafeResultsAndPublishSecretsOnce(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	initialSecret := filepath.Join(t.TempDir(), "initial-secret")
	stdout := new(bytes.Buffer)
	command := newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--confirm", "--data-dir", root, "--secret-output", initialSecret, "--json"})
	require.NoError(t, command.ExecuteContext(ctx))

	var initialized map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &initialized))
	assert.Equal(t, true, initialized["ok"])
	assert.Equal(t, "init", initialized["operation"])
	assert.NotEmpty(t, initialized["revision"])
	initialBearer, err := os.ReadFile(initialSecret)
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(initialBearer)))

	stderr := new(bytes.Buffer)
	command = newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(stderr)
	command.SetArgs([]string{"maintenance", "reset-admin-credentials", "--confirm", "--data-dir", root, "--json"})
	err = command.ExecuteContext(ctx)
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Contains(t, stderr.String(), "--secret-output")

	resetSecret := filepath.Join(t.TempDir(), "reset-secret")
	stdout.Reset()
	command = newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"maintenance", "reset-admin-credentials", "--confirm", "--data-dir", root, "--secret-output", resetSecret, "--json"})
	require.NoError(t, command.ExecuteContext(ctx))
	var reset map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &reset))
	assert.Equal(t, "reset-admin-credentials", reset["operation"])
	assert.Equal(t, initialized["installation_id"], reset["installation_id"])
	resetBearer, err := os.ReadFile(resetSecret)
	require.NoError(t, err)
	assert.NotEqual(t, initialBearer, resetBearer)
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(resetBearer)))

	guidanceSecret := filepath.Join(t.TempDir(), "guidance-secret")
	stdout.Reset()
	command = newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"maintenance", "reset-admin-credentials", "--confirm", "--data-dir", root, "--secret-output", guidanceSecret})
	require.NoError(t, command.ExecuteContext(ctx))
	assert.Contains(t, stdout.String(), "--admin-bearer-file")
	assert.Contains(t, stdout.String(), "cannot be shown again")
	assert.Contains(t, stdout.String(), "printf '%b_'")
	guidanceBearer, err := os.ReadFile(guidanceSecret)
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(guidanceBearer)))
}

func TestRestoreVerifyCurrentEmitsOneSafeMachineResult(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.NoError(t, composition.InitializeTraffic(ctx, ownership, store, "01ARZ3NDEKTSV4RRFFQ69G5FAV"))
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	stdout := new(bytes.Buffer)
	command := newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"maintenance", "verify-and-recover-storage", "--confirm", "--data-dir", root, "--json"})
	require.NoError(t, command.ExecuteContext(ctx))

	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, map[string]any{
		"ok":              true,
		"operation":       "verify-and-recover-storage",
		"installation_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"revision":        "0",
	}, result)
}

func TestRestoreBackupEmitsSafeResultAndReplacementSecret(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	initialSecret := filepath.Join(t.TempDir(), "initial")
	command := newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--confirm", "--data-dir", root, "--secret-output", initialSecret})
	require.NoError(t, command.ExecuteContext(ctx))

	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Open(ctx, ownership)
	require.NoError(t, err)
	generation, err := store.SelectedTraffic(ctx)
	require.NoError(t, err)
	identity, err := store.Identity(ctx)
	require.NoError(t, err)
	traffic, err := invocation.OpenTraffic(ctx, ownership, identity.InstallationID, generation, invocation.DefaultTrafficConfig())
	require.NoError(t, err)
	manager, err := backup.New(backup.Options{Traffic: traffic, Store: store, Layout: ownership.Layout(), Clock: systemClock{}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x55}, 128))})
	require.NoError(t, err)
	artifact, _, err := manager.Create(ctx, "authority", "restore-command")
	require.NoError(t, err)
	require.NoError(t, traffic.Close())
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	stderr := new(bytes.Buffer)
	command = newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(stderr)
	command.SetArgs([]string{"maintenance", "restore-backup", "--confirm", artifact.ID, "--data-dir", root, "--json"})
	err = command.ExecuteContext(ctx)
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Contains(t, stderr.String(), "--secret-output")

	secret := filepath.Join(t.TempDir(), "replacement")
	stdout := new(bytes.Buffer)
	command = newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"maintenance", "restore-backup", "--confirm", artifact.ID, "--data-dir", root, "--secret-output", secret, "--json"})
	require.NoError(t, command.ExecuteContext(ctx))
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, "restore-backup", result["operation"])
	assert.NotContains(t, result, "mode")
	assert.Equal(t, artifact.ID, result["backup_id"])
	published, err := os.ReadFile(secret)
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(published)))

	humanSecret := filepath.Join(t.TempDir(), "human-replacement")
	stdout.Reset()
	command = newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"maintenance", "restore-backup", "--confirm", artifact.ID, "--data-dir", root, "--secret-output", humanSecret})
	require.NoError(t, command.ExecuteContext(ctx))
	assert.Contains(t, stdout.String(), "cannot be shown again")
	humanBearer, err := os.ReadFile(humanSecret)
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), string(bytes.TrimSpace(humanBearer)))
}

func TestRestoreRejectsInvalidInvocationWithSafeMachineResult(t *testing.T) {
	for _, args := range [][]string{
		{"maintenance", "restore-backup", "--confirm", "--json"},
		{"maintenance", "restore-backup", "--confirm", "unexpected", "--json"},
		{"maintenance", "restore-backup", "--confirm", "--json", "--unknown-flag"},
		{"maintenance", "restore-backup", "--confirm", "--json", "--verify-current"},
		{"maintenance", "verify-and-recover-storage", "--confirm", "unexpected", "--json"},
		{"maintenance", "verify-and-recover-storage", "--confirm", "--json", "--secret-output", "unused"},
	} {
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		command := newTestRootCmd(t)
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs(args)
		err := command.ExecuteContext(context.Background())
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err))
		assert.Empty(t, stdout.String())
		var problem controlclient.Problem
		require.NoError(t, json.Unmarshal(stderr.Bytes(), &problem))
		assert.Equal(t, "client_invalid_input", problem.Code)
	}
}

func TestRestoreVerifyCurrentFailureIsSafeAndNonzero(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newTestRootCmd(t)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"maintenance", "verify-and-recover-storage", "--confirm", "--data-dir", root, "--json"})
	err = command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 5, commandExitCode(err))
	assert.Empty(t, stdout.String())
	var problem controlclient.Problem
	require.NoError(t, json.Unmarshal(stderr.Bytes(), &problem))
	assert.Equal(t, "gateway_running", problem.Code)
}
