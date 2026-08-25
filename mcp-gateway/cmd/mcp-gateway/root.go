package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/api"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/events"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/mcpingress"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/spf13/cobra"
)

type commandFailure struct{}

func (commandFailure) Error() string { return "command failed" }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type offlineDependencies struct {
	clock          admin.Clock
	entropy        io.Reader
	newComposition func(composition.Options) (*composition.Composition, error)
}

func newRootCmd() *cobra.Command {
	return newRootCmdWithDependencies(offlineDependencies{clock: systemClock{}, entropy: rand.Reader, newComposition: composition.New})
}

func newRootCmdWithDependencies(dependencies offlineDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:           "mcp-gateway",
		Short:         "Locally secure, deny-by-default MCP gateway",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.AddCommand(
		newAdminAuthorityCmd("initialize", dependencies),
		newAdminAuthorityCmd("admin-reset", dependencies),
		newRestoreCmd(dependencies),
		newServeCmd(dependencies),
	)
	return command
}

func newServeCmd(dependencies offlineDependencies) *cobra.Command {
	var dataDir string
	var authority string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Serve the verified local Gateway boundary",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeCommandFailure(command, "serve", "invalid_command")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if dataDir == "" {
				return writeCommandFailure(command, "serve", "invalid_command")
			}
			started, err := executeServe(command, dataDir, authority, dependencies)
			if err == nil {
				return nil
			}
			if started {
				_, _ = fmt.Fprintln(command.ErrOrStderr(), "mcp-gateway serve stopped")
				return commandFailure{}
			}
			return writeCommandFailure(command, "serve", serveErrorCode(err))
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "owner-only Gateway data directory")
	command.Flags().StringVar(&authority, "listen", contract.DefaultAuthority, "exact numeric IPv4 loopback authority")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeCommandFailure(command, "serve", "invalid_command")
	})
	return command
}

func executeServe(command *cobra.Command, dataDir, authority string, dependencies offlineDependencies) (bool, error) {
	ctx := command.Context()
	ownership, err := gatewaypaths.Acquire(dataDir)
	if err != nil {
		return false, err
	}
	defer func() { _ = ownership.Close() }()
	store, err := storage.Open(ctx, ownership)
	if err != nil {
		return false, err
	}
	defer func() { _ = store.Close() }()
	identity, err := store.Identity(ctx)
	if err != nil {
		return false, err
	}
	eventHub := events.New()
	defer eventHub.Shutdown()
	var ready, draining atomic.Bool
	newComposition := dependencies.newComposition
	if newComposition == nil {
		newComposition = composition.New
	}
	runtime, err := newComposition(composition.Options{
		Store: store, InstallationID: identity.InstallationID, CallbackURL: "http://" + authority + "/oauth/callback",
		Clock: dependencies.clock, Entropy: dependencies.entropy, Invalidate: eventHub.Publish, Ready: ready.Load,
	})
	if err != nil {
		return false, err
	}
	defer func() { <-runtime.Drain(context.Background()) }()
	serverRepository := runtime.Servers()
	catalogRepository := runtime.CatalogRepository()
	activeCatalog := runtime.ActiveCatalog()
	provider := runtime.Provider()
	keyringCoordinator := runtime.Keyring()
	flowService := runtime.OAuthFlows()
	replacementService := runtime.Replacements()
	credentials := admin.NewService(store, dependencies.clock, dependencies.entropy)
	sessions := admin.NewSessionManager(credentials, dependencies.clock, dependencies.entropy)
	defer sessions.Shutdown()
	unsubscribeEvents := credentials.SubscribeCredentialInvalidations(func(id *string) {
		eventHub.InvalidateCredential(id)
		eventHub.Publish(contract.Invalidation{Kind: contract.InvalidationAdminCredentials, ResourceID: id})
		eventHub.Publish(contract.Invalidation{Kind: contract.InvalidationSystemStatus})
	})
	defer unsubscribeEvents()
	backupManager, err := backup.New(backup.Options{Store: store, Layout: ownership.Layout(), Clock: dependencies.clock, Entropy: dependencies.entropy})
	if err != nil {
		return false, err
	}
	startedAt := dependencies.clock.Now().UTC().Format(time.RFC3339Nano)
	capabilitySnapshot := contract.KeyringUnsupported
	var boundary *httpboundary.Boundary
	ingress := mcpingress.New(mcpingress.Options{
		Authenticator: mcpingress.DenyAllAuthenticator{},
		Now:           dependencies.clock.Now,
		Entropy:       dependencies.entropy,
	})
	defer ingress.Shutdown()
	apiHandler := api.New(api.Options{
		Credentials:    credentials,
		Sessions:       sessions,
		Backups:        backupManager,
		Events:         eventHub,
		Invalidate:     eventHub.Publish,
		Origin:         "http://" + authority,
		Servers:        serverRepository,
		AuthFlows:      flowService,
		OAuthCallback:  flowService,
		Replacements:   replacementService,
		Catalog:        catalogRepository,
		ActiveCatalog:  activeCatalog,
		OperationState: runtime.OperationState,
		RuntimeStatus: func(serverID string) api.RuntimeStatus {
			status := runtime.RuntimeStatus(serverID)
			return api.RuntimeStatus{State: status.State, Reason: status.Reason, RuntimeID: status.RuntimeID, CredentialState: status.CredentialState, CatalogState: status.CatalogState, Reconciliation: status.Reconciliation}
		},
		TriggerServer:    runtime.TriggerServer,
		CatalogTraversal: runtime.CatalogServerStatus,
		DispatchStatus:   runtime.DispatchServerStatus,
		Status: func() contract.SystemStatus {
			current, identityErr := store.Identity(context.Background())
			if identityErr != nil {
				current = identity
			}
			mcpWork, mcpStreams, legacySessions := ingress.Status()
			status := baseSystemStatus(
				startedAt, current, store.Latched(), draining.Load(), capabilitySnapshot, provider.WorkStatus(), sessions.Status(),
				mcpWork, mcpStreams, legacySessions,
			)
			status.Backup = backupManager.Status()
			status.Limits.BackupWork = backupManager.WorkStatus()
			status.Limits.BackupRecords = backupManager.RecordStatus()
			status.Limits.IdempotencyRecords = backupManager.IdempotencyStatus()
			status.Limits.EventStreams = eventHub.Status()
			status.Limits.ServerReconciliations = runtime.ReconciliationStatus()
			status.Limits.DownstreamRuntimes = runtime.RuntimeOccupancy()
			status.Limits.OAuthFlows = flowService.Status(context.Background())
			status.Limits.OAuthCallbackWork = flowService.CallbackStatus()
			if identities, activeServers, registryErr := serverRepository.RegistryStatus(context.Background()); registryErr == nil {
				status.Limits.ServerIdentities = identities
				status.Limits.Servers = activeServers
			}
			if idempotency, idempotencyErr := serverRepository.IdempotencyStatus(context.Background()); idempotencyErr == nil {
				status.Limits.S2IdempotencyRecords = idempotency
			}
			if identities, identityErr := catalogRepository.IdentityStatus(context.Background()); identityErr == nil {
				status.Limits.DurableToolIdentities = identities
			}
			status.Limits.ActiveTools = activeCatalog.Occupancy()
			status.Limits.CatalogTraversals = runtime.CatalogTraversalStatus()
			status.Limits.DownstreamDispatch = runtime.DispatchStatus()
			status.Limits.AdminCredentials = credentials.Status(context.Background())
			if candidates, candidateErr := keyringCoordinator.CandidateStatus(context.Background()); candidateErr == nil {
				status.Limits.KeyringCandidates = candidates
			}
			if databaseBytes, databaseErr := store.DatabaseStatus(context.Background()); databaseErr == nil {
				status.Limits.DatabaseBytes = databaseBytes
			}
			if boundary != nil {
				status.Limits.HTTPRegular, status.Limits.HTTPControlAuth, status.Limits.HTTPAdmin, status.Limits.HTTPHealth = boundary.AdmissionStatus()
			}
			return status
		},
	})
	boundary, err = httpboundary.New(httpboundary.Options{
		Authority: authority,
		Ready:     ready.Load,
		Draining:  draining.Load,
		Authenticate: func(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			if authority == contract.AuthorityAgent {
				return ingress.Authenticate(ctx, request, authority)
			}
			return apiHandler.Authenticate(ctx, request, authority)
		},
		Next: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/mcp" {
				ingress.ServeHTTP(writer, request)
				return
			}
			apiHandler.ServeHTTP(writer, request)
		}),
	})
	if err != nil {
		return false, err
	}
	listener, capability, err := httpboundary.OpenListener(ctx, authority, func(ctx context.Context) (contract.KeyringCapability, error) {
		return provider.Probe(ctx).State, nil
	})
	if err != nil {
		return false, err
	}
	defer func() { _ = listener.Close() }()
	capabilitySnapshot = capability
	server := &http.Server{
		Handler:           boundary,
		ReadHeaderTimeout: contract.HeaderReadDeadline,
		ReadTimeout:       contract.APIHandlerDeadline,
		WriteTimeout:      contract.APIHandlerDeadline,
		MaxHeaderBytes:    limitMaximum("request_header_bytes"),
	}
	ready.Store(true)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	if err := runtime.Start(ctx); err != nil {
		return true, err
	}
	result := struct {
		OK             bool   `json:"ok"`
		Operation      string `json:"operation"`
		Authority      string `json:"authority"`
		InstallationID string `json:"installation_id"`
	}{true, "serve", authority, identity.InstallationID}
	if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
		return false, err
	}
	runtimeClean := true
	select {
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			return true, err
		}
	case <-ctx.Done():
		draining.Store(true)
		ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), contract.GracefulShutdownDeadline)
		defer cancel()
		runtimeDrain := runtime.Drain(shutdownCtx)
		eventHub.Shutdown()
		ingress.Shutdown()
		sessions.Shutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return true, err
		}
		select {
		case result := <-runtimeDrain:
			runtimeClean = result.Unconfirmed == 0
		case <-shutdownCtx.Done():
			runtimeClean = false
		}
		if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return true, err
		}
	}
	if err := store.Close(); err != nil {
		return true, err
	}
	if runtimeClean {
		if err := ownership.MarkClean(); err != nil {
			return true, err
		}
	}
	return true, nil
}

func baseSystemStatus(
	startedAt string,
	identity storage.Identity,
	latched bool,
	draining bool,
	capability contract.KeyringCapability,
	keyringWork, adminSessions, mcpWork, mcpStreams, legacySessions contract.LimitStatus,
) contract.SystemStatus {
	state := contract.SQLiteReady
	process := contract.ProcessReady
	ready := true
	if latched {
		state, process, ready = contract.SQLiteLatched, contract.ProcessStorageFailed, false
	}
	if draining {
		process, ready = contract.ProcessDraining, false
	}
	limits := contract.LimitsStatus{
		MCPWork: mcpWork, MCPStreams: mcpStreams, AdminSessions: adminSessions,
		LegacySessions: legacySessions, EventStreams: fixedStatus("event_streams", 0), BackupWork: fixedStatus("backup_work", 0),
		BackupRecords: fixedStatus("backup_records", 0), AdminCredentials: fixedStatus("admin_credentials", 0), IdempotencyRecords: fixedStatus("idempotency_records", 0),
		KeyringCandidates: fixedStatus("keyring_candidates", 0), KeyringWork: keyringWork, DatabaseBytes: fixedStatus("database_bytes", 0),
		ServerIdentities: fixedStatus("server_identities", 0), Servers: fixedStatus("servers", 0), DownstreamRuntimes: fixedStatus("downstream_runtimes", 0),
		ServerReconciliations: fixedStatus("server_reconciliations", 0), CatalogTraversals: fixedStatus("catalog_traversals", 0), OAuthFlows: fixedStatus("oauth_flows", 0),
		OAuthCallbackWork: fixedStatus("oauth_callback_work", 0), S2IdempotencyRecords: fixedStatus("s2_idempotency_records", 0), ActiveTools: fixedStatus("active_tools", 0),
		DurableToolIdentities: fixedStatus("durable_tool_identities", 0), DownstreamDispatch: fixedStatus("downstream_dispatch", 0),
	}
	return contract.SystemStatus{
		Process: contract.ProcessStatus{State: process, Ready: ready, StartedAt: startedAt},
		SQLite:  contract.SQLiteStatus{State: state, SchemaVersion: fmt.Sprintf("%d", identity.SchemaVersion), Revision: fmt.Sprintf("%d", identity.Revision), Latched: latched},
		Keyring: contract.KeyringStatus{Capability: capability}, Limits: limits, Backup: contract.BackupStatus{State: contract.BackupIdle},
		Protocols: contract.ProtocolStatus{Modern: contract.ModernProtocolVersion, Legacy: contract.LegacyProtocolVersion, AgentAuth: contract.AgentAuthDenyAll},
	}
}

func fixedStatus(name string, inUse int64) contract.LimitStatus {
	value, _ := contract.FixedLimitByName(name)
	return contract.LimitStatus{InUse: inUse, Limit: value.Maximum, Saturated: inUse >= value.Maximum}
}

func limitMaximum(name string) int {
	value, _ := contract.FixedLimitByName(name)
	return int(value.Maximum)
}

func serveErrorCode(err error) string {
	switch {
	case errors.Is(err, gatewaypaths.ErrInUse):
		return "gateway_running"
	case strings.Contains(err.Error(), "authority"):
		return "invalid_authority"
	default:
		return "storage_unavailable"
	}
}

func newAdminAuthorityCmd(operation string, dependencies offlineDependencies) *cobra.Command {
	var dataDir string
	var secretOutput string
	command := &cobra.Command{
		Use:   operation,
		Short: "Create and publish Gateway admin authority",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeCommandFailure(command, operation, "invalid_command")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if dataDir == "" {
				return writeCommandFailure(command, operation, "invalid_command")
			}
			sink := admin.NewTerminalSecretSink()
			if secretOutput != "" {
				sink = admin.NewFileSecretSink(secretOutput)
			}
			identity, err := executeAdminAuthority(command.Context(), operation, dataDir, sink, dependencies)
			if err != nil {
				return writeCommandFailure(command, operation, adminCommandErrorCode(err))
			}
			result := struct {
				OK             bool   `json:"ok"`
				Operation      string `json:"operation"`
				InstallationID string `json:"installation_id"`
				Revision       string `json:"revision"`
			}{
				OK:             true,
				Operation:      operation,
				InstallationID: identity.InstallationID,
				Revision:       fmt.Sprintf("%d", identity.Revision),
			}
			if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
				return commandFailure{}
			}
			return nil
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "owner-only Gateway data directory")
	command.Flags().StringVar(&secretOutput, "secret-output", "", "new owner-only file for the one-time admin bearer")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeCommandFailure(command, operation, "invalid_command")
	})
	return command
}

func executeAdminAuthority(
	ctx context.Context,
	operation string,
	dataDir string,
	sink admin.SecretSink,
	dependencies offlineDependencies,
) (storage.Identity, error) {
	ownership, err := gatewaypaths.AcquireForMaintenance(dataDir)
	if err != nil {
		return storage.Identity{}, fmt.Errorf("acquire stopped-process ownership: %w", err)
	}
	defer func() { _ = ownership.Close() }()

	var store *storage.Store
	if operation == "initialize" {
		installationID, idErr := admin.NewID(dependencies.clock.Now(), dependencies.entropy)
		if idErr != nil {
			return storage.Identity{}, idErr
		}
		store, err = storage.Initialize(ctx, ownership, installationID)
		if errors.Is(err, storage.ErrAlreadyInitialized) {
			store, err = storage.Open(ctx, ownership)
		}
	} else {
		store, err = storage.Open(ctx, ownership)
	}
	if err != nil {
		return storage.Identity{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = store.Close()
		}
	}()

	service := admin.NewService(store, dependencies.clock, dependencies.entropy)
	if operation == "initialize" {
		_, err = service.Initialize(ctx, sink)
	} else {
		_, err = service.Reset(ctx, sink)
	}
	if err != nil {
		return storage.Identity{}, err
	}
	identity, err := store.Identity(ctx)
	if err != nil {
		return storage.Identity{}, err
	}
	if err := store.Close(); err != nil {
		return storage.Identity{}, err
	}
	closed = true
	if err := ownership.MarkClean(); err != nil {
		return storage.Identity{}, err
	}
	return identity, nil
}

func adminCommandErrorCode(err error) string {
	switch {
	case errors.Is(err, gatewaypaths.ErrInUse):
		return "gateway_running"
	case errors.Is(err, admin.ErrAlreadyInitialized):
		return "already_initialized"
	case errors.Is(err, admin.ErrNotInitialized):
		return "not_initialized"
	case errors.Is(err, admin.ErrSecretPublication):
		return "secret_output_unavailable"
	default:
		return "storage_unavailable"
	}
}

func newRestoreCmd(dependencies offlineDependencies) *cobra.Command {
	var dataDir, secretOutput string
	var verify bool
	command := &cobra.Command{
		Use:   "restore [backup-id]",
		Short: "Verify or restore a stopped Gateway database",
		Args: func(command *cobra.Command, args []string) error {
			validVerify := verify && len(args) == 0
			validBackup := !verify && len(args) == 1 && backup.ValidID(args[0])
			if !validVerify && !validBackup {
				return writeCommandFailure(command, "restore", "invalid_command")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if dataDir == "" || (verify && secretOutput != "") {
				return writeCommandFailure(command, "restore", "invalid_command")
			}
			var identity storage.Identity
			var err error
			mode, backupID := "verify_current", ""
			if verify {
				identity, err = storage.VerifyCurrent(command.Context(), dataDir)
			} else {
				mode, backupID = "backup", args[0]
				sink := admin.NewTerminalSecretSink()
				if secretOutput != "" {
					sink = admin.NewFileSecretSink(secretOutput)
				}
				identity, err = backup.Restore(command.Context(), backup.RestoreOptions{Root: dataDir, BackupID: backupID, Sink: sink, Clock: dependencies.clock, Entropy: dependencies.entropy})
			}
			if err != nil {
				code := "storage_unavailable"
				switch {
				case errors.Is(err, gatewaypaths.ErrInUse):
					code = "gateway_running"
				case errors.Is(err, backup.ErrNotFound), errors.Is(err, backup.ErrInvalidArtifact):
					code = "invalid_backup"
				case errors.Is(err, admin.ErrSecretPublication):
					code = "secret_output_unavailable"
				}
				return writeCommandFailure(command, "restore", code)
			}
			result := struct {
				OK             bool   `json:"ok"`
				Operation      string `json:"operation"`
				Mode           string `json:"mode"`
				InstallationID string `json:"installation_id"`
				Revision       string `json:"revision"`
				BackupID       string `json:"backup_id,omitempty"`
			}{true, "restore", mode, identity.InstallationID, fmt.Sprintf("%d", identity.Revision), backupID}
			if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
				return commandFailure{}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&verify, "verify-current", false, "verify and clear a stopped installation's storage latch")
	command.Flags().StringVar(&dataDir, "data-dir", "", "owner-only Gateway data directory")
	command.Flags().StringVar(&secretOutput, "secret-output", "", "new owner-only file for the replacement admin bearer")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeCommandFailure(command, "restore", "invalid_command")
	})
	return command
}

func writeCommandFailure(command *cobra.Command, operation, code string) error {
	result := struct {
		OK        bool   `json:"ok"`
		Operation string `json:"operation"`
		Code      string `json:"code"`
	}{OK: false, Operation: operation, Code: code}
	if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
		return commandFailure{}
	}
	return commandFailure{}
}
