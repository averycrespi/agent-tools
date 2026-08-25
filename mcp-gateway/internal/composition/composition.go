// Package composition constructs the production runtime and catalog object graph.
package composition

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/credentialauthority"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

type Clock interface {
	Now() time.Time
}

type Options struct {
	Store          *storage.Store
	InstallationID string
	CallbackURL    string
	Clock          Clock
	Entropy        io.Reader
	Invalidate     func(contract.Invalidation)
	Ready          func() bool
}

var (
	ErrAlreadyStarted = errors.New("production composition is already started")
	ErrNotReady       = errors.New("control plane is not ready")
	ErrStartFailed    = errors.New("production composition start failed")
)

type Composition struct {
	servers           *servers.Repository
	catalogRepository *catalog.Repository
	activeCatalog     *catalog.ActiveRegistry
	traverser         *catalog.Traverser
	remoteFactory     *remote.Factory
	provider          *keyring.Provider
	keyring           *keyring.Coordinator
	authority         *credentialauthority.Resolver
	owner             *runtimes.RuntimeOwner
	stdio             *runtimes.StdioSupervisor
	driver            *runtimes.ConcreteDriver
	catalog           *catalog.Coordinator
	oauthResolver     *oauth.Resolver
	disconnect        *oauth.DisconnectService
	registrar         *oauth.Registrar
	flows             *oauth.FlowService
	refresh           *oauth.RefreshService
	replacements      *servercredentials.Service
	manager           *runtimes.Manager
	publisher         *activePublisher
	callbacks         *callbackSlots
	startHooks        startHooks
	startMu           sync.Mutex
	started           bool
	startFailed       bool
	accepting         atomic.Bool
	ready             func() bool
	drainMu           sync.Mutex
	drainDone         chan struct{}
	drainResult       runtimes.DrainResult
}

func (built *Composition) Servers() *servers.Repository                { return built.servers }
func (built *Composition) CatalogRepository() *catalog.Repository      { return built.catalogRepository }
func (built *Composition) ActiveCatalog() *catalog.ActiveRegistry      { return built.activeCatalog }
func (built *Composition) Traverser() *catalog.Traverser               { return built.traverser }
func (built *Composition) Provider() *keyring.Provider                 { return built.provider }
func (built *Composition) Keyring() *keyring.Coordinator               { return built.keyring }
func (built *Composition) RuntimeOwner() *runtimes.RuntimeOwner        { return built.owner }
func (built *Composition) CatalogCoordinator() *catalog.Coordinator    { return built.catalog }
func (built *Composition) OAuthFlows() *oauth.FlowService              { return built.flows }
func (built *Composition) Replacements() *servercredentials.Service    { return built.replacements }
func (built *Composition) DisconnectService() *oauth.DisconnectService { return built.disconnect }
func (built *Composition) RefreshService() *oauth.RefreshService       { return built.refresh }
func (built *Composition) RuntimeStatus(serverID string) runtimes.Status {
	return built.manager.Status(serverID)
}
func (built *Composition) OperationState(ctx context.Context, serverID string) servers.OperationTriggerState {
	return built.manager.OperationState(ctx, serverID)
}
func (built *Composition) TriggerServer(serverID string, operationID *string, behavioral bool) {
	built.manager.Trigger(serverID, operationID, behavioral)
}
func (built *Composition) ReconciliationStatus() contract.LimitStatus {
	return built.manager.AdmissionStatus()
}
func (built *Composition) RuntimeOccupancy() contract.LimitStatus { return built.owner.Status() }
func (built *Composition) CatalogTraversalStatus() contract.LimitStatus {
	return built.catalog.Status()
}
func (built *Composition) CatalogServerStatus(serverID string) contract.LimitStatus {
	return built.catalog.ServerStatus(serverID)
}
func (built *Composition) DispatchStatus() contract.LimitStatus {
	return built.activeCatalog.Routes().Status()
}
func (built *Composition) DispatchServerStatus(serverID string) contract.LimitStatus {
	return built.activeCatalog.Routes().ServerStatus(serverID)
}

type startHooks struct {
	afterBind         func(*Composition) error
	beforeReconstruct func(*Composition) error
}

type callbackSlots struct {
	mu         sync.RWMutex
	isBound    bool
	currentFn  func(runtimes.Candidate) bool
	clientFn   func(runtimes.Candidate) (catalog.PageClient, bool)
	reportFn   func(runtimes.Candidate, runtimes.FailureDisposition) bool
	completeFn func(runtimes.Candidate, runtimes.CatalogOutcome, *string) bool
	stateFn    func(string, contract.ServerCredentialState, bool)
	triggerFn  func(string)
	fenceFn    func(string)
	runningFn  func() bool
}

func (slots *callbackSlots) bind(built *Composition) bool {
	slots.mu.Lock()
	defer slots.mu.Unlock()
	if slots.isBound {
		return false
	}
	slots.currentFn = built.manager.Current
	slots.clientFn = func(candidate runtimes.Candidate) (catalog.PageClient, bool) {
		return built.driver.Runtime(candidate)
	}
	slots.reportFn = built.manager.ReportRuntimeFailure
	slots.completeFn = built.manager.HandleCatalogCompletion
	slots.stateFn = built.manager.SetCredentialState
	slots.triggerFn = func(serverID string) { built.manager.Trigger(serverID, nil, true) }
	slots.fenceFn = built.manager.Fence
	slots.runningFn = built.accepting.Load
	slots.isBound = true
	return true
}

func (slots *callbackSlots) bound() bool {
	slots.mu.RLock()
	defer slots.mu.RUnlock()
	return slots.isBound
}

func (slots *callbackSlots) current(candidate runtimes.Candidate) bool {
	slots.mu.RLock()
	callback := slots.currentFn
	slots.mu.RUnlock()
	return callback != nil && callback(candidate)
}

func (slots *callbackSlots) client(candidate runtimes.Candidate) (catalog.PageClient, bool) {
	slots.mu.RLock()
	callback := slots.clientFn
	slots.mu.RUnlock()
	if callback == nil {
		return nil, false
	}
	return callback(candidate)
}

func (slots *callbackSlots) report(candidate runtimes.Candidate, disposition runtimes.FailureDisposition) bool {
	slots.mu.RLock()
	callback := slots.reportFn
	slots.mu.RUnlock()
	return callback != nil && callback(candidate, disposition)
}

func (slots *callbackSlots) complete(candidate runtimes.Candidate, outcome runtimes.CatalogOutcome, operationID *string) bool {
	slots.mu.RLock()
	callback := slots.completeFn
	slots.mu.RUnlock()
	return callback != nil && callback(candidate, outcome, operationID)
}

func (slots *callbackSlots) state(serverID string, state contract.ServerCredentialState, withdraw bool) {
	slots.mu.RLock()
	callback := slots.stateFn
	slots.mu.RUnlock()
	if callback != nil {
		callback(serverID, state, withdraw)
	}
}

func (slots *callbackSlots) trigger(serverID string) {
	slots.mu.RLock()
	callback := slots.triggerFn
	slots.mu.RUnlock()
	if callback != nil {
		callback(serverID)
	}
}

func (slots *callbackSlots) fence(serverID string) {
	slots.mu.RLock()
	callback := slots.fenceFn
	slots.mu.RUnlock()
	if callback != nil {
		callback(serverID)
	}
}

func (slots *callbackSlots) running() bool {
	slots.mu.RLock()
	callback := slots.runningFn
	slots.mu.RUnlock()
	return callback != nil && callback()
}

type systemScheduler struct{}
type systemTimer struct{ timer *time.Timer }

func (systemScheduler) AfterFunc(delay time.Duration, callback func()) runtimes.Timer {
	return systemTimer{timer: time.AfterFunc(delay, callback)}
}
func (timer systemTimer) Stop() bool { return timer.timer.Stop() }

type activePublisher struct {
	mu     sync.Mutex
	active *catalog.ActiveRegistry
	fences map[string]uint64
}

func (publisher *activePublisher) Fence(serverID string, generation uint64) {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if generation > publisher.fences[serverID] {
		publisher.fences[serverID] = generation
	}
}

func (publisher *activePublisher) current(candidate runtimes.Candidate) bool {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return candidate.Generation >= publisher.fences[candidate.Server.ID]
}

func (publisher *activePublisher) Withdraw(candidate runtimes.Candidate) {
	publisher.active.WithdrawExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, contract.ActiveCatalogUnavailable)
}

type constructorHooks struct {
	before         func(string) error
	provider       func(string) (*keyring.Provider, error)
	startStdio     runtimes.StdioStarter
	newCoordinator runtimes.CoordinatorFactory
	startHooks     startHooks
}

var mandatoryConstructorStages = []string{
	"server_repository", "catalog_repository", "process_id", "active_registry", "traverser", "remote_factory",
	"provider", "keyring_coordinator", "authority_resolver", "oauth_resolver", "disconnect_service", "registrar",
	"flow_service", "runtime_owner", "stdio_supervisor", "driver", "catalog_coordinator", "refresh_service",
	"replacement_service", "manager",
}

func New(options Options) (*Composition, error) {
	return newWithHooks(options, constructorHooks{})
}

func newWithHooks(options Options, hooks constructorHooks) (_ *Composition, resultErr error) {
	if options.Store == nil || options.InstallationID == "" || options.CallbackURL == "" || options.Clock == nil || options.Entropy == nil || options.Invalidate == nil || options.Ready == nil {
		return nil, errors.New("production composition dependencies are incomplete")
	}
	check := func(stage string) error {
		if hooks.before == nil {
			return nil
		}
		if err := hooks.before(stage); err != nil {
			return fmt.Errorf("construct %s: %w", stage, err)
		}
		return nil
	}
	built := &Composition{callbacks: &callbackSlots{}, startHooks: hooks.startHooks, ready: options.Ready}
	cleanup := true
	defer func() {
		if cleanup {
			built.shutdownConstructed()
		}
	}()
	if err := check("server_repository"); err != nil {
		return nil, err
	}
	var err error
	built.servers, err = servers.New(options.Store, options.Clock, options.Entropy)
	if err != nil {
		return nil, fmt.Errorf("construct server_repository: %w", err)
	}
	authority, err := authorization.New(options.Store, options.Clock, options.Entropy)
	if err != nil {
		return nil, fmt.Errorf("construct authorization validation: %w", err)
	}
	if err := authority.ValidateStartup(context.Background(), built.servers); err != nil {
		return nil, fmt.Errorf("validate authorization startup: %w", err)
	}
	if err := check("catalog_repository"); err != nil {
		return nil, err
	}
	built.catalogRepository, err = catalog.NewRepository(options.Store, options.Clock, options.Entropy)
	if err != nil {
		return nil, fmt.Errorf("construct catalog_repository: %w", err)
	}
	if err := check("process_id"); err != nil {
		return nil, err
	}
	processID, err := built.servers.NewID()
	if err != nil {
		return nil, fmt.Errorf("construct process_id: %w", err)
	}
	if err := check("active_registry"); err != nil {
		return nil, err
	}
	built.activeCatalog, err = catalog.NewActiveRegistry(built.catalogRepository, options.Clock, processID)
	if err != nil {
		return nil, fmt.Errorf("construct active_registry: %w", err)
	}
	built.publisher = &activePublisher{active: built.activeCatalog, fences: make(map[string]uint64)}
	if err := check("traverser"); err != nil {
		return nil, err
	}
	built.traverser = catalog.NewTraverser()
	if err := check("remote_factory"); err != nil {
		return nil, err
	}
	built.remoteFactory = remote.New(remote.Options{})
	if err := check("provider"); err != nil {
		return nil, err
	}
	providerFactory := hooks.provider
	if providerFactory == nil {
		providerFactory = productionProvider
	}
	built.provider, err = providerFactory(options.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("construct provider: %w", err)
	}
	if err := check("keyring_coordinator"); err != nil {
		return nil, err
	}
	built.keyring = keyring.NewCoordinator(built.provider, options.Store, options.Clock, options.Entropy)
	if err := check("authority_resolver"); err != nil {
		return nil, err
	}
	built.authority, err = credentialauthority.New(built.servers, built.keyring, options.InstallationID, options.Clock.Now)
	if err != nil {
		return nil, fmt.Errorf("construct authority_resolver: %w", err)
	}
	if err := check("oauth_resolver"); err != nil {
		return nil, err
	}
	built.oauthResolver, err = oauth.NewResolver(built.remoteFactory)
	if err != nil {
		return nil, fmt.Errorf("construct oauth_resolver: %w", err)
	}
	if err := check("disconnect_service"); err != nil {
		return nil, err
	}
	built.disconnect, err = oauth.NewDisconnectService(built.servers, built.keyring, built.oauthResolver, built.remoteFactory, options.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("construct disconnect_service: %w", err)
	}
	if err := check("registrar"); err != nil {
		return nil, err
	}
	built.registrar, err = oauth.NewRegistrar(built.remoteFactory, built.servers, built.keyring, options.InstallationID, options.Clock.Now, built.callbacks.running)
	if err != nil {
		return nil, fmt.Errorf("construct registrar: %w", err)
	}
	if err := check("flow_service"); err != nil {
		return nil, err
	}
	built.flows, err = oauth.NewFlowService(built.servers, built.oauthResolver, built.registrar, built.remoteFactory, built.keyring, options.InstallationID, options.Entropy, options.CallbackURL, options.Clock.Now)
	if err != nil {
		return nil, fmt.Errorf("construct flow_service: %w", err)
	}
	if err := check("runtime_owner"); err != nil {
		return nil, err
	}
	built.owner = runtimes.NewRuntimeOwner()
	if err := check("stdio_supervisor"); err != nil {
		return nil, err
	}
	built.stdio = runtimes.NewStdioSupervisor(options.Clock.Now)
	if err := check("driver"); err != nil {
		return nil, err
	}
	startStdio := hooks.startStdio
	if startStdio == nil {
		startStdio = func(ctx context.Context, definition runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			return built.stdio.Start(ctx, definition)
		}
	}
	built.driver, err = runtimes.NewConcreteDriver(runtimes.ConcreteDriverOptions{
		Owner:          built.owner,
		StartStdio:     startStdio,
		HTTPFactory:    built.remoteFactory,
		NewCoordinator: hooks.newCoordinator,
		ReportFailure:  built.callbacks.report,
	})
	if err != nil {
		return nil, fmt.Errorf("construct driver: %w", err)
	}
	if err := check("catalog_coordinator"); err != nil {
		return nil, err
	}
	built.catalog, err = catalog.NewCoordinator(catalog.CoordinatorOptions{
		InstallationID: options.InstallationID,
		Repository:     built.catalogRepository,
		Active:         built.activeCatalog,
		Traverser:      built.traverser,
		Clock:          options.Clock,
		Scheduler:      systemScheduler{},
		Client:         built.callbacks.client,
		Current: func(candidate runtimes.Candidate) bool {
			return built.publisher.current(candidate) && built.callbacks.current(candidate)
		},
		Complete: func(candidate runtimes.Candidate, outcome runtimes.CatalogOutcome, operationID *string) {
			built.callbacks.complete(candidate, outcome, operationID)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("construct catalog_coordinator: %w", err)
	}
	if err := check("refresh_service"); err != nil {
		return nil, err
	}
	built.refresh, err = oauth.NewRefreshService(built.servers, built.keyring, built.oauthResolver, built.remoteFactory, options.InstallationID, options.Clock.Now, built.callbacks.state, built.callbacks.trigger)
	if err != nil {
		return nil, fmt.Errorf("construct refresh_service: %w", err)
	}
	if err := check("replacement_service"); err != nil {
		return nil, err
	}
	built.replacements, err = servercredentials.New(built.servers, built.keyring, options.InstallationID, built.callbacks.fence, built.callbacks.trigger)
	if err != nil {
		return nil, fmt.Errorf("construct replacement_service: %w", err)
	}
	if err := check("manager"); err != nil {
		return nil, err
	}
	built.manager, err = runtimes.New(runtimes.Options{
		Repository:   built.servers,
		Driver:       built.driver,
		Authority:    built.authority,
		Catalog:      built.catalog,
		Credentials:  built.disconnect,
		OAuthRefresh: built.refresh,
		OAuthStepUp:  built.flows,
		Invalidate:   options.Invalidate,
		Publisher:    built.publisher,
	})
	if err != nil {
		return nil, fmt.Errorf("construct manager: %w", err)
	}
	cleanup = false
	return built, nil
}

func (built *Composition) Start(ctx context.Context) error {
	built.startMu.Lock()
	defer built.startMu.Unlock()
	if built.started {
		return ErrAlreadyStarted
	}
	if built.startFailed {
		return ErrStartFailed
	}
	if !built.ready() {
		return ErrNotReady
	}
	if !built.callbacks.bind(built) {
		built.startFailed = true
		return ErrStartFailed
	}
	if built.startHooks.afterBind != nil {
		if err := built.startHooks.afterBind(built); err != nil {
			built.startFailed = true
			return fmt.Errorf("bind production composition: %w", err)
		}
	}
	if built.startHooks.beforeReconstruct != nil {
		if err := built.startHooks.beforeReconstruct(built); err != nil {
			built.startFailed = true
			return fmt.Errorf("start production reconstruction: %w", err)
		}
	}
	if err := built.flows.Start(ctx); err != nil {
		built.accepting.Store(false)
		built.startFailed = true
		return fmt.Errorf("start OAuth flows: %w", err)
	}
	built.accepting.Store(true)
	if err := built.manager.Start(ctx); err != nil {
		built.accepting.Store(false)
		built.startFailed = true
		return fmt.Errorf("start runtime reconstruction: %w", err)
	}
	built.started = true
	return nil
}

func (built *Composition) Drain(ctx context.Context) <-chan runtimes.DrainResult {
	result := make(chan runtimes.DrainResult, 1)
	if built == nil {
		close(result)
		return result
	}
	built.drainMu.Lock()
	if built.drainDone == nil {
		built.drainDone = make(chan struct{})
		built.beginDrain(ctx)
	}
	done := built.drainDone
	built.drainMu.Unlock()
	go func() {
		select {
		case <-done:
			built.drainMu.Lock()
			result <- built.drainResult
			built.drainMu.Unlock()
		case <-ctx.Done():
			result <- runtimes.DrainResult{Unconfirmed: 1}
		}
		close(result)
	}()
	return result
}

func (built *Composition) beginDrain(ctx context.Context) {
	built.accepting.Store(false)
	ownedBefore := int64(0)
	if built.owner != nil {
		ownedBefore = built.owner.Status().InUse
	}
	if built.activeCatalog != nil {
		built.activeCatalog.Drain()
	}
	if built.catalog != nil {
		built.catalog.Shutdown()
	}
	if built.refresh != nil {
		built.refresh.Shutdown()
	}
	if built.flows != nil {
		built.flows.Shutdown()
	}
	if built.keyring != nil {
		built.keyring.Drain()
	}
	var managerDone <-chan runtimes.DrainResult
	if built.manager != nil {
		managerDone = built.manager.Drain(ctx)
	}
	go built.awaitDrain(ctx, ownedBefore, managerDone)
}

func (built *Composition) awaitDrain(ctx context.Context, ownedBefore int64, managerDone <-chan runtimes.DrainResult) {
	waits := make(chan bool, 5)
	waitCount := 0
	for _, wait := range []func(context.Context) bool{
		func(ctx context.Context) bool { return built.manager == nil || built.manager.Wait(ctx) },
		func(ctx context.Context) bool { return built.catalog == nil || built.catalog.Wait(ctx) },
		func(ctx context.Context) bool { return built.refresh == nil || built.refresh.Wait(ctx) },
		func(ctx context.Context) bool { return built.flows == nil || built.flows.Wait(ctx) },
		func(ctx context.Context) bool { return built.keyring == nil || built.keyring.Wait(ctx) },
	} {
		waitCount++
		wait := wait
		go func() { waits <- wait(ctx) }()
	}
	clean := true
	result := runtimes.DrainResult{}
	if managerDone != nil {
		select {
		case result = <-managerDone:
		case <-ctx.Done():
			clean = false
		}
	}
	for range waitCount {
		if !<-waits {
			clean = false
		}
	}
	if built.owner != nil {
		ownedAfter := built.owner.Status().InUse
		if ownedAfter <= ownedBefore {
			result.Verified = int(ownedBefore - ownedAfter)
			result.Unconfirmed = int(ownedAfter)
		}
	}
	if !clean && result.Unconfirmed == 0 {
		result.Unconfirmed = 1
	}
	built.drainMu.Lock()
	built.drainResult = result
	close(built.drainDone)
	built.drainMu.Unlock()
}

func (built *Composition) shutdownConstructed() {
	if built == nil {
		return
	}
	<-built.Drain(context.Background())
}
