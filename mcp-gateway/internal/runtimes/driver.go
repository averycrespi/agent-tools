package runtimes

import (
	"context"
	"errors"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type StdioStarter func(context.Context, StdioDefinition) (downstream.StdioRuntime, error)
type CoordinatorFactory func(downstream.Transport) (*downstream.Coordinator, error)
type NegotiatorFactory func(downstream.OpenCoordinator) (*downstream.Negotiator, error)
type FailureReporter func(Candidate, FailureDisposition) bool

type ConcreteDriverOptions struct {
	Owner          *RuntimeOwner
	StartStdio     StdioStarter
	HTTPFactory    *remote.Factory
	NewCoordinator CoordinatorFactory
	NewNegotiator  NegotiatorFactory
	ReportFailure  FailureReporter
}

type ConcreteDriver struct {
	mu             sync.Mutex
	owner          *RuntimeOwner
	startStdio     StdioStarter
	httpFactory    *remote.Factory
	newCoordinator CoordinatorFactory
	newNegotiator  NegotiatorFactory
	reportFailure  FailureReporter
	handles        map[CandidateKey]*concreteHandle
}

type concreteHandle struct {
	mu             sync.Mutex
	transport      downstream.Transport
	coordinator    *downstream.Coordinator
	runtime        *downstream.Runtime
	stop           func(context.Context) bool
	closeAttempted bool
	stopping       bool
	released       bool
	failureOnce    sync.Once
	failureCancel  context.CancelFunc
	stdioDone      <-chan StdioExit
	authorization  string
}

func NewConcreteDriver(options ConcreteDriverOptions) (*ConcreteDriver, error) {
	if options.Owner == nil || options.StartStdio == nil || options.HTTPFactory == nil {
		return nil, errors.New("concrete runtime driver dependencies are incomplete")
	}
	if options.NewCoordinator == nil {
		options.NewCoordinator = downstream.NewCoordinator
	}
	if options.NewNegotiator == nil {
		options.NewNegotiator = downstream.NewNegotiator
	}
	if options.ReportFailure == nil {
		options.ReportFailure = func(Candidate, FailureDisposition) bool { return false }
	}
	return &ConcreteDriver{owner: options.Owner, startStdio: options.StartStdio, httpFactory: options.HTTPFactory, newCoordinator: options.NewCoordinator, newNegotiator: options.NewNegotiator, reportFailure: options.ReportFailure, handles: make(map[CandidateKey]*concreteHandle)}, nil
}

func (driver *ConcreteDriver) Reconcile(ctx context.Context, candidate Candidate, lease *MaterialLease) Outcome {
	var desired contract.Transport
	var initial *downstream.Coordinator
	key, err := driver.owner.Admit(candidate, lease, func(OwnedRuntime) error {
		var constructErr error
		desired, constructErr = servers.DecodeTransport(candidate.Server.Transport)
		if constructErr != nil {
			return constructErr
		}
		initial, constructErr = driver.construct(ctx, candidate, desired)
		return constructErr
	})
	if err != nil {
		if key != (CandidateKey{}) {
			driver.cleanupConstruction(ctx, key)
		}
		return constructionFailure(err)
	}
	if !driver.owner.Transition(key, RuntimeNegotiating) {
		driver.cleanupConstruction(ctx, key)
		return constructionFailure(errors.New("candidate ownership changed during construction"))
	}
	opened := false
	negotiator, err := driver.newNegotiator(func(context.Context) (*downstream.Coordinator, error) {
		if !opened {
			opened = true
			return initial, nil
		}
		if !driver.discardVerifiedProbe(key, initial) {
			return nil, ErrCandidateOwned
		}
		return driver.construct(ctx, candidate, desired)
	})
	if err != nil {
		driver.cleanupConstruction(ctx, key)
		return constructionFailure(err)
	}
	selected, err := negotiator.Negotiate(ctx, replayNegotiationMode(desired, candidate.OAuthReplayStage))
	if err != nil {
		var challenge *downstream.OAuthChallengeDisposition
		if !errors.As(err, &challenge) {
			driver.cleanupConstruction(ctx, key)
		}
		return constructionFailure(err)
	}
	if !driver.attachRuntime(key, selected) || !driver.owner.Transition(key, RuntimeCataloging) {
		driver.cleanupConstruction(ctx, key)
		return constructionFailure(errors.New("candidate ownership changed during negotiation"))
	}
	driver.watchFailures(candidate, key, selected)
	return Outcome{State: contract.RuntimeActive, CredentialState: contract.ServerCredentialReady, CatalogState: contract.ActiveCatalogAbsent}
}

func (driver *ConcreteDriver) construct(ctx context.Context, candidate Candidate, desired contract.Transport) (*downstream.Coordinator, error) {
	switch transport := desired.(type) {
	case contract.StdioTransport:
		return driver.constructStdio(ctx, candidate, transport)
	case contract.StreamableHTTPTransport:
		return driver.constructHTTP(candidate, transport)
	default:
		return nil, servers.ErrInvalidInput
	}
}

func (driver *ConcreteDriver) constructStdio(ctx context.Context, candidate Candidate, desired contract.StdioTransport) (*downstream.Coordinator, error) {
	secrets := make(map[string]string)
	defer clearStringMap(secrets)
	if len(desired.SecretEnvironment) != 0 {
		material, ok := driver.owner.Material(candidate.Key(), contract.ServerCredentialStatic)
		if !ok {
			return nil, ErrMaterialLease
		}
		generation, err := servercredentials.DecodeStaticGeneration(material)
		if err != nil {
			return nil, err
		}
		for slot, value := range generation.Values {
			secrets[slot] = value
		}
	}
	process, err := driver.startStdio(ctx, StdioDefinition{
		RuntimeID: candidate.RuntimeID, Executable: desired.Executable, Arguments: append([]string(nil), desired.Arguments...),
		WorkingDirectory: desired.WorkingDirectory, Environment: cloneStrings(desired.Environment),
		SecretEnvironment: cloneStrings(desired.SecretEnvironment), Secrets: secrets,
	})
	if err != nil {
		return nil, err
	}
	transport, err := downstream.NewStdioTransport(process)
	if err != nil {
		if !process.Stop(ctx) {
			driver.storeHandle(candidate.Key(), &concreteHandle{stop: process.Stop})
		}
		return nil, err
	}
	var done <-chan StdioExit
	if source, ok := process.(interface{ Done() <-chan StdioExit }); ok {
		done = source.Done()
	}
	return driver.installTransport(candidate.Key(), transport, "", process.Stop, done)
}

func (driver *ConcreteDriver) constructHTTP(candidate Candidate, desired contract.StreamableHTTPTransport) (*downstream.Coordinator, error) {
	authorization := ""
	allowLoopbackHTTP := false
	switch desired.Authentication.(type) {
	case contract.NoAuthentication:
		allowLoopbackHTTP = true
	case contract.BearerAuthentication:
		material, ok := driver.owner.Material(candidate.Key(), contract.ServerCredentialStatic)
		if !ok {
			return nil, ErrMaterialLease
		}
		generation, err := servercredentials.DecodeStaticGeneration(material)
		if err != nil || generation.Values["bearer"] == "" {
			return nil, ErrMaterialLease
		}
		authorization = "Bearer " + generation.Values["bearer"]
	case contract.OAuthAuthentication:
		accessToken, ok := driver.owner.Material(candidate.Key(), contract.ServerCredentialOAuthTokens)
		if !ok || len(accessToken) == 0 {
			return nil, ErrMaterialLease
		}
		authorization = "Bearer " + string(accessToken)
	default:
		return nil, servers.ErrInvalidInput
	}
	endpoint, err := remote.ParseEndpoint(desired.URL, allowLoopbackHTTP)
	if err != nil {
		return nil, err
	}
	transport, err := downstream.NewHTTPTransport(driver.httpFactory, endpoint, authorization)
	if err != nil {
		return nil, err
	}
	return driver.installTransport(candidate.Key(), transport, authorization, func(ctx context.Context) bool { return transport.Close(ctx) == nil }, nil)
}

func (driver *ConcreteDriver) installTransport(key CandidateKey, transport downstream.Transport, authorization string, stop func(context.Context) bool, stdioDone <-chan StdioExit) (*downstream.Coordinator, error) {
	handle := &concreteHandle{transport: transport, stop: stop, stdioDone: stdioDone, authorization: authorization}
	if !driver.storeHandle(key, handle) {
		_ = transport.Close(context.Background())
		return nil, ErrCandidateOwned
	}
	coordinator, err := driver.newCoordinator(transport)
	if err != nil {
		return nil, err
	}
	handle.mu.Lock()
	handle.coordinator = coordinator
	handle.mu.Unlock()
	return coordinator, nil
}

func (driver *ConcreteDriver) discardVerifiedProbe(key CandidateKey, coordinator *downstream.Coordinator) bool {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	handle := driver.handles[key]
	if handle == nil {
		return false
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.coordinator != coordinator || handle.runtime != nil || handle.released {
		return false
	}
	handle.released = true
	delete(driver.handles, key)
	return true
}

func (driver *ConcreteDriver) attachRuntime(key CandidateKey, runtime *downstream.Runtime) bool {
	if runtime == nil {
		return false
	}
	driver.mu.Lock()
	handle := driver.handles[key]
	driver.mu.Unlock()
	if handle == nil {
		return false
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.runtime != nil {
		return false
	}
	handle.runtime = runtime
	return true
}

func (driver *ConcreteDriver) watchFailures(candidate Candidate, key CandidateKey, runtime *downstream.Runtime) {
	driver.mu.Lock()
	handle := driver.handles[key]
	driver.mu.Unlock()
	if handle == nil {
		return
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	handle.mu.Lock()
	if handle.stopping || handle.runtime != runtime {
		handle.mu.Unlock()
		cancel()
		return
	}
	handle.failureCancel = cancel
	stdioDone := handle.stdioDone
	handle.mu.Unlock()
	go func() {
		select {
		case <-watchCtx.Done():
			return
		case exit, ok := <-stdioDone:
			if ok {
				driver.reportHandleFailure(candidate, key, handle, FailureDisposition{State: contract.RuntimeDegraded, Reason: exit.Reason, Retryable: exit.Retryable, RuntimeLost: true})
			}
		case err := <-runtime.Failures():
			if stdioDone != nil {
				select {
				case exit, ok := <-stdioDone:
					if ok {
						driver.reportHandleFailure(candidate, key, handle, FailureDisposition{State: contract.RuntimeDegraded, Reason: exit.Reason, Retryable: exit.Retryable, RuntimeLost: true})
					}
					return
				default:
				}
			}
			driver.reportHandleFailure(candidate, key, handle, ClassifyFailure(err))
		}
	}()
}

func (driver *ConcreteDriver) reportHandleFailure(candidate Candidate, key CandidateKey, handle *concreteHandle, failure FailureDisposition) {
	driver.mu.Lock()
	current := driver.handles[key] == handle
	driver.mu.Unlock()
	if !current {
		return
	}
	handle.mu.Lock()
	stopping := handle.stopping
	handle.mu.Unlock()
	if stopping || !validRuntimeFailure(failure) {
		return
	}
	handle.failureOnce.Do(func() {
		if !driver.reportFailure(candidate, failure) {
			driver.Stop(context.Background(), candidate)
		}
	})
}

func negotiationMode(desired contract.Transport) downstream.Mode {
	if http, ok := desired.(contract.StreamableHTTPTransport); ok {
		return downstream.Mode(http.ProtocolMode)
	}
	return downstream.ModeAuto
}

func replayNegotiationMode(desired contract.Transport, stage downstream.OAuthChallengeStage) downstream.Mode {
	switch stage {
	case downstream.OAuthChallengeModernDiscovery:
		return downstream.ModeModern
	case downstream.OAuthChallengeLegacyInitialize:
		return downstream.ModeLegacy
	default:
		return negotiationMode(desired)
	}
}

func (driver *ConcreteDriver) storeHandle(key CandidateKey, handle *concreteHandle) bool {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if _, exists := driver.handles[key]; exists {
		return false
	}
	driver.handles[key] = handle
	return true
}

func (driver *ConcreteDriver) cleanupConstruction(ctx context.Context, key CandidateKey) {
	if !driver.hasHandle(key) {
		driver.owner.Release(key, true)
		return
	}
	if driver.stop(ctx, key) {
		driver.owner.Release(key, true)
		return
	}
	driver.owner.Release(key, false)
}

func (driver *ConcreteDriver) hasHandle(key CandidateKey) bool {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.handles[key] != nil
}

func (driver *ConcreteDriver) Stop(ctx context.Context, candidate Candidate) bool {
	key := candidate.Key()
	if !driver.stop(ctx, key) {
		driver.owner.Release(key, false)
		return false
	}
	return driver.owner.Release(key, true)
}

func (driver *ConcreteDriver) stop(ctx context.Context, key CandidateKey) bool {
	driver.mu.Lock()
	handle := driver.handles[key]
	driver.mu.Unlock()
	if handle == nil {
		return false
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.released {
		return false
	}
	handle.stopping = true
	if handle.failureCancel != nil {
		handle.failureCancel()
	}
	verified := false
	switch {
	case !handle.closeAttempted && handle.runtime != nil:
		handle.closeAttempted = true
		verified = handle.runtime.Close(ctx) == nil
	case handle.stop != nil:
		verified = handle.stop(ctx)
	case handle.transport != nil:
		verified = handle.transport.Close(ctx) == nil
	}
	if !verified {
		return false
	}
	handle.released = true
	driver.mu.Lock()
	if driver.handles[key] == handle {
		delete(driver.handles, key)
	}
	driver.mu.Unlock()
	return true
}

func (driver *ConcreteDriver) Owned(candidate Candidate) bool {
	_, ok := driver.owner.Phase(candidate.Key())
	return ok
}

func (driver *ConcreteDriver) Runtime(candidate Candidate) (*downstream.Runtime, bool) {
	driver.mu.Lock()
	handle := driver.handles[candidate.Key()]
	driver.mu.Unlock()
	if handle == nil {
		return nil, false
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.runtime, handle.runtime != nil
}

func (driver *ConcreteDriver) Coordinator(candidate Candidate) (*downstream.Coordinator, bool) {
	driver.mu.Lock()
	handle := driver.handles[candidate.Key()]
	driver.mu.Unlock()
	if handle == nil {
		return nil, false
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.coordinator, handle.coordinator != nil
}

func constructionFailure(err error) Outcome {
	var challenge *downstream.OAuthChallengeDisposition
	if errors.As(err, &challenge) {
		reason := contract.ReasonAuthenticationRejected
		return Outcome{State: contract.RuntimeAuthenticationRequired, CredentialState: contract.ServerCredentialUnavailable, CatalogState: contract.ActiveCatalogAbsent, Reason: &reason, OAuthChallenge: challenge}
	}
	failure := ClassifyFailure(err)
	return Outcome{State: failure.State, CredentialState: contract.ServerCredentialUnavailable, CatalogState: contract.ActiveCatalogAbsent, Reason: &failure.Reason, Retryable: failure.Retryable}
}

func cloneStrings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func clearStringMap(values map[string]string) {
	for key := range values {
		values[key] = ""
		delete(values, key)
	}
}
