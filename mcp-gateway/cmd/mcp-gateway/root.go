package main

import (
	"context"
	"crypto/rand"
	"database/sql"
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
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/events"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/mcpingress"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
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
		Short:         "Run and administer the local deny-by-default MCP Gateway",
		Example:       "  mcp-gateway initialize\n  mcp-gateway serve\n  mcp-gateway status",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	configureNamespaceCommand(command)
	command.PersistentFlags().String("data-dir", "", "owner-only Gateway data directory")
	command.AddCommand(
		newAdminAuthorityCmd("initialize", dependencies),
		newRestoreCmd(dependencies),
		newServeCmd(dependencies),
	)
	for _, online := range newOnlineCommands() {
		if online.Name() == "admin" {
			online.AddCommand(newAdminAuthorityCmd("reset", dependencies))
		}
		command.AddCommand(online)
	}
	return command
}

func newServeCmd(dependencies offlineDependencies) *cobra.Command {
	var dataDir, authority, output string
	var jsonOutput bool
	command := &cobra.Command{
		Use:     "serve",
		Short:   "Start the local Gateway service",
		Example: "  mcp-gateway serve",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeOfflineProblem(command, selectedOutputMode(command, output, jsonOutput), offlineUsageProblem("The serve command does not accept positional arguments.", "mcp-gateway serve"))
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			options, err := resolveExecutionOptions(executionOptionInput{DataDir: selectedDataDir(command, dataDir), Output: output, OutputSet: command.Flags().Changed("output"), JSON: jsonOutput})
			if err != nil {
				return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("Choose either --output human or --output json; --json is the JSON shorthand.", "mcp-gateway serve"))
			}
			layout, err := gatewaypaths.Resolve(options.DataDir)
			if err != nil {
				return writeOfflineProblem(command, options.Output, controlclient.NewInputError("The selected data directory is invalid."))
			}
			renderer, err := controlclient.NewRenderer(options.Output, command.OutOrStdout(), command.ErrOrStderr())
			if err != nil {
				return commandFailure{}
			}
			phases := controlclient.NewServePhases(renderer)
			acknowledged, err := executeServe(command, layout.Root, authority, dependencies, phases)
			if err == nil {
				return nil
			}
			problem := serveCommandProblem(err, acknowledged, layout.Root)
			if phases.WriteProblem(problem) != nil {
				return commandFailure{}
			}
			return problem
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "owner-only Gateway data directory")
	command.Flags().StringVar(&authority, "listen", contract.DefaultAuthority, "exact numeric IPv4 loopback authority")
	command.Flags().StringVar(&output, "output", "human", "output mode: human or json")
	command.Flags().BoolVar(&jsonOutput, "json", false, "shorthand for --output json")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeOfflineProblem(command, selectedOutputMode(command, output, jsonOutput), offlineUsageProblem("A serve flag is invalid or incomplete.", "mcp-gateway serve"))
	})
	return command
}

func selectedDataDir(command *cobra.Command, local string) string {
	if command != nil && command.LocalNonPersistentFlags().Changed("data-dir") {
		return local
	}
	if command != nil {
		root, err := command.Root().PersistentFlags().GetString("data-dir")
		if err == nil && root != "" {
			return root
		}
	}
	return local
}

func executeServe(command *cobra.Command, dataDir, authority string, dependencies offlineDependencies, phases *controlclient.ServePhases) (bool, error) {
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
	authorizationRepository := runtime.Authorization()
	if authorizationRepository == nil {
		return false, errors.New("production authorization owner is unavailable")
	}
	agentIngress, ok := runtime.AgentIngress()
	if !ok {
		return false, errors.New("production agent ingress owner is unavailable")
	}
	controlAPI, ok := runtime.ControlAPI()
	if !ok {
		return false, errors.New("production control API owner is unavailable")
	}
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
		Authenticator: agentIngress.Authenticator,
		ListTools:     agentIngress.ListTools,
		CallTools:     agentIngress.CallTools,
		Now:           dependencies.clock.Now,
		Entropy:       dependencies.entropy,
	})
	defer ingress.Shutdown()
	apiHandler := api.New(api.Options{
		InstallationID: identity.InstallationID,

		Credentials:   credentials,
		Sessions:      sessions,
		Backups:       backupManager,
		Events:        eventHub,
		Invalidate:    eventHub.Publish,
		Origin:        "http://" + authority,
		Servers:       serverRepository,
		Principals:    authorizationRepository,
		GrantRequests: controlAPI.GrantRequests,
		Invocations:   controlAPI.Invocations,
		Audit:         controlAPI.Audit,

		AuthorizationCollections: controlAPI.AuthorizationCollections,

		GrantTarget: func(ctx context.Context, transaction *sql.Tx, serverID string) (bool, error) {
			_, validateErr := serverRepository.ValidateGrantTargetTx(ctx, transaction, serverID)
			if errors.Is(validateErr, serverdomain.ErrNotFound) {
				return false, nil
			}
			return validateErr == nil, validateErr
		},
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
				startedAt, current, store.Latched(), draining.Load(), agentIngress.AuthMode, capabilitySnapshot, provider.WorkStatus(), sessions.Status(),
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
			if principals, grants, authorizationErr := runtime.AuthorizationOccupancy(context.Background()); authorizationErr == nil {
				status.Limits.Principals = principals
				status.Limits.Grants = grants
			}
			if requests, evidence, requestErr := runtime.GrantRequestOccupancy(context.Background()); requestErr == nil {
				status.Limits.GrantRequests = requests
				status.Limits.GrantRequestEvidenceBytes = evidence
			}
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
		AuthenticatedProblem: apiHandler.RecordAuthenticatedProblem,
		Authority:            authority,
		Ready:                ready.Load,
		Draining:             draining.Load,
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
	stopBeforeAcknowledgement := func() {
		ready.Store(false)
		_ = server.Close()
		<-serveDone
	}
	select {
	case err := <-serveDone:
		ready.Store(false)
		return false, err
	default:
	}
	if err := runtime.Start(ctx); err != nil {
		stopBeforeAcknowledgement()
		return false, err
	}
	select {
	case err := <-serveDone:
		ready.Store(false)
		return false, err
	default:
	}
	result := struct {
		OK             bool   `json:"ok"`
		Operation      string `json:"operation"`
		Authority      string `json:"authority"`
		InstallationID string `json:"installation_id"`
	}{true, "serve", authority, identity.InstallationID}
	encoded, err := json.Marshal(result)
	if err != nil {
		stopBeforeAcknowledgement()
		return false, err
	}
	human := "Gateway started successfully.\nWeb address: http://" + controlclient.TerminalSafePath(authority) + "/\nInstallation: " + identity.InstallationID
	if err := phases.Acknowledge(encoded, human); err != nil {
		stopBeforeAcknowledgement()
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
		if !runtimeClean {
			drainErr := shutdownCtx.Err()
			if drainErr == nil {
				drainErr = errors.New("production composition drain remained unconfirmed")
			}
			return true, fmt.Errorf("production composition drain: %w", drainErr)
		}
		eventHub.Shutdown()
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
	agentAuth contract.AgentAuthMode,
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
		Principals: fixedStatus("principals", 0), Grants: fixedStatus("grants", 0),
		GrantRequests: fixedStatus("grant_requests", 0), GrantRequestEvidenceBytes: fixedStatus("grant_request_evidence_bytes", 0),
	}
	return contract.SystemStatus{
		Process: contract.ProcessStatus{State: process, Ready: ready, StartedAt: startedAt},
		SQLite:  contract.SQLiteStatus{State: state, SchemaVersion: fmt.Sprintf("%d", identity.SchemaVersion), Revision: fmt.Sprintf("%d", identity.Revision), Latched: latched},
		Keyring: contract.KeyringStatus{Capability: capability}, Limits: limits, Backup: contract.BackupStatus{State: contract.BackupIdle},
		Protocols: contract.ProtocolStatus{Modern: contract.ModernProtocolVersion, Legacy: contract.LegacyProtocolVersion, AgentAuth: agentAuth},
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

func serveCommandProblem(err error, acknowledged bool, dataDir string) *controlclient.Problem {
	if acknowledged {
		return &controlclient.Problem{Code: "serve_stopped", Title: "The Gateway stopped after startup because clean shutdown could not be confirmed. The installation remains marked unclean.", Exit: 7}
	}
	switch serveErrorCode(err) {
	case "gateway_running":
		return &controlclient.Problem{Code: "gateway_running", Title: "Another Gateway process owns the selected installation. Stop it or choose a different data directory.", Exit: 5}
	case "invalid_authority":
		return &controlclient.Problem{Code: "invalid_authority", Title: "The listen address is invalid. Use an exact numeric IPv4 loopback authority such as 127.0.0.1:8210.", Exit: 2}
	default:
		return &controlclient.Problem{Code: "storage_unavailable", Title: "The Gateway installation at " + boundedProblemPath(dataDir, "the selected data directory") + " could not be started safely.", Exit: 7}
	}
}

func newAdminAuthorityCmd(operation string, dependencies offlineDependencies) *cobra.Command {
	var dataDir, secretOutput, output string
	var jsonOutput bool
	short := "Create a new local Gateway installation"
	example := "  mcp-gateway initialize"
	usage := "mcp-gateway initialize"
	if operation == "reset" {
		short = "Replace all administrator authority for a stopped Gateway"
		example = "  mcp-gateway admin reset --secret-output NEW_PATH"
		usage = "mcp-gateway admin reset --secret-output NEW_PATH"
	}
	command := &cobra.Command{
		Use:     operation,
		Short:   short,
		Long:    short + ". The one-time administrator bearer is written to a new non-symlink 0600 owner-only file and cannot be recovered after publication.",
		Example: example,
		Args: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return writeOfflineProblem(command, selectedOutputMode(command, output, jsonOutput), offlineUsageProblem("The "+operation+" command does not accept positional arguments.", usage))
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			options, err := resolveExecutionOptions(executionOptionInput{DataDir: selectedDataDir(command, dataDir), Output: output, OutputSet: command.Flags().Changed("output"), JSON: jsonOutput})
			if err != nil {
				return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("Choose either --output human or --output json; --json is the JSON shorthand.", usage))
			}
			layout, err := gatewaypaths.Resolve(options.DataDir)
			if err != nil {
				return writeOfflineProblem(command, options.Output, controlclient.NewInputError("The selected data directory is invalid."))
			}
			secretPath := secretOutput
			if operation == "initialize" && secretPath == "" {
				secretPath = layout.AdminBearer
			}
			if secretPath == "" {
				return writeOfflineProblem(command, options.Output, offlineUsageProblem("The --secret-output flag is required for administrator authority replacement.", usage))
			}
			startCommand, err := renderServeCommand(layout.Root, dataDir == "")
			if err != nil {
				return writeOfflineProblem(command, options.Output, controlclient.NewInputError("The selected data directory is too long to render safely."))
			}
			identity, err := executeAdminAuthority(command.Context(), operation, layout.Root, admin.NewFileSecretSink(secretPath), dependencies)
			if err != nil {
				return writeOfflineProblem(command, options.Output, adminCommandProblem(operation, adminCommandErrorCode(err), layout.Root, secretPath, startCommand))
			}
			result := struct {
				OK             bool   `json:"ok"`
				Operation      string `json:"operation"`
				InstallationID string `json:"installation_id"`
				Revision       string `json:"revision"`
				DataDir        string `json:"data_dir,omitempty"`
				BearerFile     string `json:"admin_bearer_file,omitempty"`
			}{OK: true, Operation: operation, InstallationID: identity.InstallationID, Revision: fmt.Sprintf("%d", identity.Revision)}
			if operation == "initialize" {
				result.DataDir = layout.Root
				result.BearerFile = secretPath
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return commandFailure{}
			}
			human := "Gateway initialized successfully.\nData directory: " + controlclient.TerminalSafePath(layout.Root) + "\nAdministrator bearer file: " + controlclient.TerminalSafePath(secretPath) + "\nBearer published once to the owner-only file; it cannot be shown again.\nStart the Gateway: " + startCommand + "\nOpen: http://127.0.0.1:8210/"
			if operation != "initialize" {
				bearerCommand, renderErr := renderBearerCommand("mcp-gateway status", secretPath)
				if renderErr != nil {
					return writeOfflineProblem(command, options.Output, controlclient.NewInputError("The secret output path is too long to render safely."))
				}
				human = "Administrator authority replaced successfully.\nNew bearer file: " + controlclient.TerminalSafePath(secretPath) + "\nBearer published once to the owner-only file; it cannot be shown again.\nUse the replacement explicitly: " + bearerCommand
			}
			renderer, err := controlclient.NewRenderer(options.Output, command.OutOrStdout(), command.ErrOrStderr())
			if err != nil || renderer.WriteFiniteSuccess(encoded, human) != nil {
				return commandFailure{}
			}
			return nil
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "owner-only Gateway data directory")
	command.Flags().StringVar(&secretOutput, "secret-output", "", "new non-symlink 0600 owner-only file for the one-time admin bearer")
	command.Flags().StringVar(&output, "output", "human", "output mode: human or json")
	command.Flags().BoolVar(&jsonOutput, "json", false, "shorthand for --output json")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeOfflineProblem(command, selectedOutputMode(command, output, jsonOutput), offlineUsageProblem("A "+operation+" flag is invalid or incomplete.", usage))
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

func selectedOutputMode(command *cobra.Command, output string, jsonOutput bool) controlclient.OutputMode {
	options, err := resolveExecutionOptions(executionOptionInput{Output: output, OutputSet: command.Flags().Changed("output"), JSON: jsonOutput})
	if err != nil {
		return controlclient.OutputHuman
	}
	return options.Output
}

func writeOfflineProblem(command *cobra.Command, mode controlclient.OutputMode, problem *controlclient.Problem) error {
	renderer, err := controlclient.NewRenderer(mode, command.OutOrStdout(), command.ErrOrStderr())
	if err != nil || renderer.WriteProblem(problem) != nil {
		return commandFailure{}
	}
	return problem
}

func adminCommandProblem(operation, code, dataDir, secretPath, startCommand string) *controlclient.Problem {
	safeDataDir := boundedProblemPath(dataDir, "the selected data directory")
	safeSecretPath := boundedProblemPath(secretPath, "the selected output path")
	switch code {
	case "gateway_running":
		return &controlclient.Problem{Code: code, Title: "The Gateway is running. Stop it before changing stopped-process administrator authority.", Exit: 5}
	case "already_initialized":
		return &controlclient.Problem{Code: code, Title: "The Gateway installation at " + safeDataDir + " is already initialized. Start it with: " + startCommand, Exit: 5}
	case "not_initialized":
		return &controlclient.Problem{Code: code, Title: "The Gateway installation at " + safeDataDir + " is not initialized. Run mcp-gateway initialize first.", Exit: 4}
	case "secret_output_unavailable":
		return &controlclient.Problem{Code: code, Title: "The administrator bearer could not be published to " + safeSecretPath + ". Choose a new nonexistent owner-only output path; authority was not activated.", Exit: 2}
	default:
		return &controlclient.Problem{Code: code, Title: "The stopped-process " + operation + " operation could not access the Gateway installation safely.", Exit: 7}
	}
}

func boundedProblemPath(path, fallback string) string {
	safe := controlclient.TerminalSafePath(path)
	if len(safe) > 200 {
		return fallback
	}
	return safe
}

func newRestoreCmd(dependencies offlineDependencies) *cobra.Command {
	var dataDir, secretOutput, output string
	var verify, jsonOutput bool
	command := &cobra.Command{
		Use:   "restore [backup-id]",
		Short: "Verify or restore a stopped Gateway database",
		Long:  "Verify or restore a stopped Gateway database. Restore writes its one-time replacement administrator bearer to a new non-symlink 0600 owner-only file; the bearer cannot be recovered after publication.",
		Example: "  mcp-gateway restore --verify-current\n" +
			"  mcp-gateway restore BACKUP_ID --secret-output NEW_PATH",
		Args: func(command *cobra.Command, args []string) error {
			validVerify := verify && len(args) == 0
			validBackup := !verify && len(args) == 1 && backup.ValidID(args[0])
			if !validVerify && !validBackup {
				return writeOfflineProblem(command, selectedOutputMode(command, output, jsonOutput), offlineUsageProblem("Use --verify-current with no backup ID, or provide exactly one valid backup ID.", "mcp-gateway restore --verify-current | mcp-gateway restore BACKUP_ID --secret-output NEW_PATH"))
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			options, err := resolveExecutionOptions(executionOptionInput{DataDir: selectedDataDir(command, dataDir), Output: output, OutputSet: command.Flags().Changed("output"), JSON: jsonOutput})
			if err != nil {
				return writeOfflineProblem(command, controlclient.OutputHuman, offlineUsageProblem("Choose either --output human or --output json; --json is the JSON shorthand.", "mcp-gateway restore --verify-current | mcp-gateway restore BACKUP_ID --secret-output NEW_PATH"))
			}
			layout, err := gatewaypaths.Resolve(options.DataDir)
			if err != nil {
				return writeOfflineProblem(command, options.Output, controlclient.NewInputError("The selected data directory is invalid."))
			}
			if verify && secretOutput != "" {
				return writeOfflineProblem(command, options.Output, controlclient.NewInputError("--secret-output cannot be used with --verify-current."))
			}
			if !verify && secretOutput == "" {
				return writeOfflineProblem(command, options.Output, offlineUsageProblem("The --secret-output flag is required when restoring a backup.", "mcp-gateway restore BACKUP_ID --secret-output NEW_PATH"))
			}
			var identity storage.Identity
			mode, backupID := "verify_current", ""
			if verify {
				identity, err = storage.VerifyCurrent(command.Context(), layout.Root)
			} else {
				mode, backupID = "backup", args[0]
				identity, err = backup.Restore(command.Context(), backup.RestoreOptions{Root: layout.Root, BackupID: backupID, Sink: admin.NewFileSecretSink(secretOutput), Clock: dependencies.clock, Entropy: dependencies.entropy})
			}
			if err != nil {
				return writeOfflineProblem(command, options.Output, restoreCommandProblem(err, layout.Root, secretOutput))
			}
			result := struct {
				OK             bool   `json:"ok"`
				Operation      string `json:"operation"`
				Mode           string `json:"mode"`
				InstallationID string `json:"installation_id"`
				Revision       string `json:"revision"`
				BackupID       string `json:"backup_id,omitempty"`
			}{true, "restore", mode, identity.InstallationID, fmt.Sprintf("%d", identity.Revision), backupID}
			encoded, err := json.Marshal(result)
			if err != nil {
				return commandFailure{}
			}
			human := "Gateway installation verified successfully.\nData directory: " + controlclient.TerminalSafePath(layout.Root)
			if !verify {
				bearerCommand, renderErr := renderBearerCommand("mcp-gateway status", secretOutput)
				if renderErr != nil {
					return writeOfflineProblem(command, options.Output, controlclient.NewInputError("The secret output path is too long to render safely."))
				}
				human = "Gateway backup " + backupID + " restored successfully.\nReplacement bearer file: " + controlclient.TerminalSafePath(secretOutput) + "\nBearer published once to the owner-only file; it cannot be shown again.\nUse the replacement explicitly: " + bearerCommand
			}
			renderer, err := controlclient.NewRenderer(options.Output, command.OutOrStdout(), command.ErrOrStderr())
			if err != nil || renderer.WriteFiniteSuccess(encoded, human) != nil {
				return commandFailure{}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&verify, "verify-current", false, "verify and clear a stopped installation's storage latch")
	command.Flags().StringVar(&dataDir, "data-dir", "", "owner-only Gateway data directory")
	command.Flags().StringVar(&secretOutput, "secret-output", "", "new non-symlink 0600 owner-only file for the replacement admin bearer")
	command.Flags().StringVar(&output, "output", "human", "output mode: human or json")
	command.Flags().BoolVar(&jsonOutput, "json", false, "shorthand for --output json")
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeOfflineProblem(command, selectedOutputMode(command, output, jsonOutput), offlineUsageProblem("A restore flag is invalid or incomplete.", "mcp-gateway restore --verify-current | mcp-gateway restore BACKUP_ID --secret-output NEW_PATH"))
	})
	return command
}

func offlineUsageProblem(title, usage string) *controlclient.Problem {
	return controlclient.NewInputError(title + " Usage: " + usage)
}

func restoreCommandProblem(err error, dataDir, secretPath string) *controlclient.Problem {
	switch {
	case errors.Is(err, gatewaypaths.ErrInUse):
		return &controlclient.Problem{Code: "gateway_running", Title: "The Gateway is running. Stop it before verifying or restoring the installation.", Exit: 5}
	case errors.Is(err, backup.ErrNotFound), errors.Is(err, backup.ErrInvalidArtifact):
		return &controlclient.Problem{Code: "invalid_backup", Title: "The selected backup is unavailable or invalid. List the installation's backups and choose a valid backup ID.", Exit: 4}
	case errors.Is(err, admin.ErrSecretPublication):
		return &controlclient.Problem{Code: "secret_output_unavailable", Title: "The replacement administrator bearer could not be published to " + boundedProblemPath(secretPath, "the selected output path") + ". Choose a new nonexistent owner-only output path; restored authority was not installed.", Exit: 2}
	default:
		return &controlclient.Problem{Code: "storage_unavailable", Title: "The Gateway installation at " + boundedProblemPath(dataDir, "the selected data directory") + " could not be verified or restored safely.", Exit: 7}
	}
}
