package composition

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/discovery"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/mcpingress"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var compositionTime = time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)

func TestNewBuildsOneFailClosedProductionGraph(t *testing.T) {
	assert.Nil(t, (*Composition)(nil).ListTools())
	_, available := (*Composition)(nil).AgentIngress()
	assert.False(t, available)
	options, cleanup := newCompositionOptions(t)
	defer cleanup()

	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	require.NotNil(t, built.servers)
	require.NotNil(t, built.authorization)
	assert.Same(t, built.authorization, built.Authorization())
	require.NotNil(t, built.catalogRepository)
	require.NotNil(t, built.activeCatalog)
	require.NotNil(t, built.discovery)
	assert.Same(t, built.discovery, built.Discovery())
	require.NotNil(t, built.discoveryCursors)
	require.NotNil(t, built.discoveryPager)
	require.NotNil(t, built.listTools)
	assert.Same(t, built.listTools, built.ListTools())
	require.NotNil(t, built.invocationRepository)
	require.NotNil(t, built.invocationPipelines)
	require.NotNil(t, built.invocationService)
	require.NotNil(t, built.callTools)
	assert.Same(t, built.invocationService, built.callTools.service)
	assert.Same(t, built.invocationPipelines, built.callTools.pipelines)
	agentIngress, ok := built.AgentIngress()
	require.True(t, ok)
	assert.Same(t, built.authorization, agentIngress.Authenticator)
	assert.Same(t, built.listTools, agentIngress.ListTools)
	assert.Same(t, built.callTools, agentIngress.CallTools)
	assert.Equal(t, contract.AgentAuthPrincipalCredentials, agentIngress.AuthMode)
	require.NotNil(t, built.traverser)
	require.NotNil(t, built.remoteFactory)
	require.NotNil(t, built.provider)
	require.NotNil(t, built.keyring)
	require.NotNil(t, built.authority)
	require.NotNil(t, built.owner)
	require.NotNil(t, built.stdio)
	require.NotNil(t, built.driver)
	require.NotNil(t, built.catalog)
	require.NotNil(t, built.oauthResolver)
	require.NotNil(t, built.disconnect)
	require.NotNil(t, built.registrar)
	require.NotNil(t, built.flows)
	require.NotNil(t, built.refresh)
	require.NotNil(t, built.replacements)
	require.NotNil(t, built.manager)
	require.NotNil(t, built.publisher)
	assert.Same(t, built.activeCatalog, built.publisher.active)
	assert.False(t, built.callbacks.bound())

	candidate := runtimes.Candidate{}
	assert.False(t, built.callbacks.current(candidate))
	_, ok = built.callbacks.client(candidate)
	assert.False(t, ok)
	assert.False(t, built.callbacks.report(candidate, runtimes.FailureDisposition{}))
	assert.False(t, built.callbacks.complete(candidate, runtimes.CatalogOutcome{}, nil))
	assert.False(t, built.callbacks.running())
	candidate.Server.ID = "server"
	candidate.Generation = 1
	assert.True(t, built.publisher.current(candidate))
	built.publisher.Fence("server", 2)
	assert.False(t, built.publisher.current(candidate))
	built.callbacks.state("server", contract.ServerCredentialReady, true)
	built.callbacks.trigger("server")
	built.callbacks.fence("server")
}

func TestAuthorityOwnerExposesOccupancyAndDrainsBeforeCompositionCompletes(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	authority := built.Authorization()
	created, err := authority.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{
		DisplayName: "composition principal",
		Visibility:  contract.VisibilityAll,
	})
	require.NoError(t, err)
	issued, err := authority.IssueCredential(context.Background(), created.Principal.ID, created.Principal.Revision)
	require.NoError(t, err)
	lease, err := authority.Authenticate(context.Background(), issued.Bearer)
	require.NoError(t, err)
	principals, grants, err := built.AuthorizationOccupancy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), principals.InUse)
	assert.Equal(t, int64(1), grants.InUse)

	<-built.Drain(context.Background())
	select {
	case <-lease.Done():
	default:
		t.Fatal("composition drain completed before its authority lease was canceled")
	}
	_, err = authority.Authenticate(context.Background(), issued.Bearer)
	assert.ErrorIs(t, err, authorization.ErrShuttingDown)
	_, err = built.ListTools().ListTools(t.Context(), lease, "", func(context.Context, any, string) ([]byte, error) { return nil, nil })
	assert.ErrorIs(t, err, mcpingress.ErrToolsListAuthorizationUnavailable)
}

func TestPositiveAgentIngressUsesComposedAuthorityDiscoveryAndDrainFence(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	agentIngress, ok := built.AgentIngress()
	require.True(t, ok)
	ingress := mcpingress.New(mcpingress.Options{
		Authenticator: agentIngress.Authenticator,
		ListTools:     agentIngress.ListTools,
		CallTools:     agentIngress.CallTools,
	})
	defer ingress.Shutdown()

	unknownBearer := contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	unknown := newAgentRequest(unknownBearer, `{"jsonrpc":"2.0","id":"unknown","method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	_, err = ingress.Authenticate(unknown.Context(), unknown, contract.AuthorityAgent)
	assert.Error(t, err)

	created, err := built.Authorization().CreatePrincipal(t.Context(), authorization.CreatePrincipalRequest{
		DisplayName: "discovery principal", Visibility: contract.VisibilityAll,
	})
	require.NoError(t, err)
	issued, err := built.Authorization().IssueCredential(t.Context(), created.Principal.ID, created.Principal.Revision)
	require.NoError(t, err)
	request := newAgentRequest(issued.Bearer, `{"jsonrpc":"2.0","id":"positive","method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	authenticated, err := ingress.Authenticate(request.Context(), request, contract.AuthorityAgent)
	require.NoError(t, err)
	response := httptest.NewRecorder()
	ingress.ServeHTTP(response, request.WithContext(authenticated))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"positive","result":{"tools":[]}}`, response.Body.String())

	call := newAgentRequest(issued.Bearer, `{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"missing.tool","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	authenticated, err = ingress.Authenticate(call.Context(), call, contract.AuthorityAgent)
	require.NoError(t, err)
	response = httptest.NewRecorder()
	ingress.ServeHTTP(response, call.WithContext(authenticated))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var callError struct {
		Error struct {
			Data contract.AgentCallErrorData `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &callError))
	assert.Equal(t, contract.CallRejected, callError.Error.Data.Code)
	require.NotNil(t, callError.Error.Data.InvocationID)
	record, found, err := built.invocationRepository.Read(t.Context(), *callError.Error.Data.InvocationID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, contract.AdmissionUnknownTool, record.AdmissionClass)
	assert.Equal(t, created.Principal.ID, record.PrincipalID)
	assert.Equal(t, "2026-08-25T01:00:00.000000000Z", record.AdmittedAt)

	assert.Equal(t, runtimes.DrainResult{}, <-built.Drain(context.Background()))
	countBefore, err := built.invocationRepository.Count(t.Context())
	require.NoError(t, err)
	rejected := agentIngress.CallTools.Call(t.Context(), nil, mcpingress.ToolsCallRequest{})
	assert.Equal(t, contract.AuditUnavailable, rejected.ErrorCode)
	countAfter, err := built.invocationRepository.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, countBefore, countAfter, "drained invocation adapter wrote a new audit row")
	drained := newAgentRequest(issued.Bearer, `{"jsonrpc":"2.0","id":"drained","method":"tools/list"}`)
	_, err = ingress.Authenticate(drained.Context(), drained, contract.AuthorityAgent)
	assert.Error(t, err)
}

func TestDiscoveryCursorEntropyFailureStopsConstruction(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := newWithHooks(options, constructorHooks{discoveryEntropy: strings.NewReader("")})
	require.Error(t, err)
	assert.Nil(t, built)
	assert.ErrorContains(t, err, "construct discovery_cursor")
}

func TestDrainSynchronouslyFencesAuthorizationAndDiscoveryBeforeGateQuiescence(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	releasePipeline, entered := built.invocationPipelines.TryEnter()
	require.True(t, entered)
	defer releasePipeline()
	created, err := built.Authorization().CreatePrincipal(t.Context(), authorization.CreatePrincipalRequest{
		DisplayName: "drain principal", Visibility: contract.VisibilityAll,
	})
	require.NoError(t, err)
	issued, err := built.Authorization().IssueCredential(t.Context(), created.Principal.ID, created.Principal.Revision)
	require.NoError(t, err)
	lease, err := built.Authorization().Authenticate(t.Context(), issued.Bearer)
	require.NoError(t, err)
	projection, err := built.Discovery().Project(t.Context(), discovery.Request{Lease: lease})
	require.NoError(t, err)
	cursor, err := built.discoveryCursors.Encode(discovery.CursorState{
		Snapshot: projection.Snapshot,
		Method:   discovery.CursorMethodToolsList,
		Position: 1,
	})
	require.NoError(t, err)

	admissionEntered := make(chan struct{})
	releaseAdmission := make(chan struct{})
	admissionDone := make(chan error, 1)
	go func() {
		admissionDone <- built.Authorization().WithAdmission(context.Background(), lease, func(*authorization.Admission) error {
			close(admissionEntered)
			<-releaseAdmission
			return nil
		})
	}()
	<-admissionEntered

	drainReturned := make(chan (<-chan runtimes.DrainResult), 1)
	go func() { drainReturned <- built.Drain(context.Background()) }()
	require.Eventually(t, func() bool {
		_, authenticateErr := built.Authorization().Authenticate(t.Context(), issued.Bearer)
		return errors.Is(authenticateErr, authorization.ErrShuttingDown)
	}, time.Second, time.Millisecond)
	_, entered = built.invocationPipelines.TryEnter()
	assert.False(t, entered, "composition drain did not fence new invocation pipelines")

	var drained <-chan runtimes.DrainResult
	select {
	case drained = <-drainReturned:
	case <-time.After(time.Second):
		close(releaseAdmission)
		<-admissionDone
		t.Fatal("composition drain waited for authority quiescence before returning its result channel")
	}
	select {
	case <-lease.Done():
	default:
		close(releaseAdmission)
		<-admissionDone
		t.Fatal("composition drain left the pre-admission lease open")
	}
	assert.False(t, built.ActiveCatalog().IsCurrentGeneration(projection.Snapshot.Generation))
	encoded := false
	_, err = built.ListTools().ListTools(t.Context(), lease, cursor, func(context.Context, any, string) ([]byte, error) {
		encoded = true
		return []byte(`{"tools":[]}`), nil
	})
	assert.ErrorIs(t, err, mcpingress.ErrToolsListAuthorizationUnavailable)
	assert.False(t, encoded)
	joined := built.Drain(context.Background())
	select {
	case <-drained:
		t.Fatal("composition drain completed before the authority gate quiesced")
	default:
	}
	select {
	case <-joined:
		t.Fatal("joined drain completed before the authority gate quiesced")
	default:
	}

	close(releaseAdmission)
	require.NoError(t, <-admissionDone)
	select {
	case <-drained:
		t.Fatal("composition drain completed before the invocation pipeline quiesced")
	default:
	}
	releasePipeline()
	assert.Equal(t, runtimes.DrainResult{}, <-drained)
	assert.Equal(t, runtimes.DrainResult{}, <-joined)
	_, err = options.Store.DatabaseStatus(t.Context())
	require.NoError(t, err, "composition drain must finish before root closes storage")
}

func TestStartRequiresReadinessAndBindsExactlyOnce(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	ready := false
	options.Ready = func() bool { return ready }
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()

	assert.ErrorIs(t, built.Start(context.Background()), ErrNotReady)
	assert.False(t, built.callbacks.bound())
	ready = true
	require.NoError(t, built.Start(context.Background()))
	assert.True(t, built.callbacks.bound())
	assert.True(t, built.callbacks.running())
	assert.ErrorIs(t, built.Start(context.Background()), ErrAlreadyStarted)
}

func TestStartBindsBeforeReconstructionAndIsolatesServerFailure(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	var afterBind, beforeReconstruct bool
	hooks := constructorHooks{startHooks: startHooks{
		afterBind: func(built *Composition) error {
			afterBind = true
			assert.True(t, built.callbacks.bound())
			assert.False(t, built.callbacks.running())
			assert.Equal(t, contract.RuntimeInactive, built.manager.Status("not-started").State)
			return nil
		},
		beforeReconstruct: func(built *Composition) error {
			beforeReconstruct = true
			assert.True(t, built.callbacks.bound())
			assert.False(t, built.callbacks.running())
			return nil
		},
	}}
	built, err := newWithHooks(options, hooks)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	failed := createCompositionServer(t, built.servers, "failed", true, "/definitely/not-an-mcp-server")
	inactive := createCompositionServer(t, built.servers, "inactive", false, "/bin/true")

	require.NoError(t, built.Start(context.Background()))
	assert.True(t, afterBind)
	assert.True(t, beforeReconstruct)
	assert.Equal(t, contract.RuntimeInactive, built.manager.Status(inactive.ID).State)
	require.Eventually(t, func() bool {
		status := built.manager.Status(failed.ID)
		return status.State == contract.RuntimeDegraded && status.Reason != nil
	}, 2*time.Second, time.Millisecond)
}

func TestStartBarrierFailureRunsNoReconstructionAndCannotRetry(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := newWithHooks(options, constructorHooks{startHooks: startHooks{beforeReconstruct: func(*Composition) error {
		return errors.New("blocked before reconstruction")
	}}})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	server := createCompositionServer(t, built.servers, "blocked", true, "/definitely/not-an-mcp-server")

	require.ErrorContains(t, built.Start(context.Background()), "blocked before reconstruction")
	assert.False(t, built.callbacks.running())
	assert.Equal(t, contract.RuntimeInactive, built.manager.Status(server.ID).State)
	assert.Zero(t, built.owner.Status().InUse)
	assert.Equal(t, contract.ActiveCatalogAbsent, built.activeCatalog.Status(server.ID).State)
	assert.ErrorIs(t, built.Start(context.Background()), ErrStartFailed)
}

func TestNewFailsEveryMandatoryConstructor(t *testing.T) {
	for _, stage := range mandatoryConstructorStages {
		t.Run(stage, func(t *testing.T) {
			options, cleanup := newCompositionOptions(t)
			defer cleanup()
			built, err := newWithHooks(options, constructorHooks{before: func(current string) error {
				if current == stage {
					return errors.New("injected constructor failure")
				}
				return nil
			}})
			require.Error(t, err)
			assert.Nil(t, built)
			assert.True(t, strings.Contains(err.Error(), stage), err.Error())
		})
	}
}

func TestConstructionRejectsInvalidAuthorizationStateBeforeStartup(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	require.NoError(t, options.Store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`DELETE FROM synthetic_server_identity`)
		return err
	}))

	built, err := New(options)
	require.Error(t, err)
	assert.Nil(t, built)
	assert.ErrorIs(t, err, authorization.ErrInvalidState)
}

func TestConstructionRejectsInvalidInvocationStateBeforeStartup(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	require.NoError(t, options.Store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`INSERT INTO invocations (
			id, principal_id, credential_id, credential_fingerprint, credential_revision, admitted_at, admission_class
		) VALUES (
			'01J60000000000000000000001', '01J60000000000000000000002', '01J60000000000000000000003',
			'0123456789abcdef', 1, 'not-canonical', 'invalid_params'
		)`)
		return err
	}))

	built, err := New(options)
	require.Error(t, err)
	assert.Nil(t, built)
	assert.ErrorIs(t, err, invocation.ErrInvalidState)
}

func TestConstructionRejectsLatchedAuthorizationBeforeStartup(t *testing.T) {
	options, cleanup := newCompositionOptionsWithFault(t, func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit {
			return assert.AnError
		}
		return nil
	})
	defer cleanup()
	err := options.Store.Mutate(context.Background(), func(*sql.Tx) error { return nil })
	assert.ErrorIs(t, err, storage.ErrStorageLatched)

	built, err := New(options)
	require.Error(t, err)
	assert.Nil(t, built)
	assert.ErrorIs(t, err, authorization.ErrStorageUnavailable)
}

func createCompositionServer(t *testing.T, repository *servers.Repository, namespace string, enabled bool, executable string) servers.Server {
	t.Helper()
	digest := sha256.Sum256([]byte(namespace))
	result, err := repository.Create(context.Background(), servers.CreateRequest{
		Definition: servers.Definition{Namespace: namespace, DisplayName: namespace, Enabled: enabled, Transport: contract.StdioTransport{
			Kind: contract.TransportStdio, Executable: executable, Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{},
		}},
		Idempotency: &servers.IdempotencyRequest{AuthorityID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Method: "POST", Route: "/api/v1/servers", Key: namespace, RequestHash: digest},
	})
	require.NoError(t, err)
	return result.Server
}

func newAgentRequest(bearer, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", contract.MediaTypeJSON)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	return request
}

func newCompositionOptions(t *testing.T) (Options, func()) {
	t.Helper()
	return newCompositionOptionsWithFault(t, nil)
}

func newCompositionOptionsWithFault(t *testing.T, fault func(storage.FaultPoint) error) (Options, func()) {
	t.Helper()
	dataDir := t.TempDir()
	require.NoError(t, os.Chmod(dataDir, 0o700))
	ownership, err := gatewaypaths.Acquire(dataDir)
	require.NoError(t, err)
	store, err := storage.InitializeWithFaultInjection(context.Background(), ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV", fault)
	require.NoError(t, err)
	identity, err := store.Identity(context.Background())
	require.NoError(t, err)
	entropy := make([]byte, 8192)
	for index := range entropy {
		entropy[index] = byte(index%251 + 1)
	}
	return Options{
			Store:          store,
			InstallationID: identity.InstallationID,
			CallbackURL:    "http://127.0.0.1:47100/oauth/callback",
			Clock:          testutil.NewFakeClock(compositionTime),
			Entropy:        testutil.NewFakeEntropy(entropy),
			Invalidate:     func(contract.Invalidation) {},
			Ready:          func() bool { return true },
		}, func() {
			require.NoError(t, store.Close())
			require.NoError(t, ownership.Close())
		}
}
