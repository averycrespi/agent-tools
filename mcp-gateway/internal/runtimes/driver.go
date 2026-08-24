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

type ConcreteDriverOptions struct {
	Owner          *RuntimeOwner
	StartStdio     StdioStarter
	HTTPFactory    *remote.Factory
	NewCoordinator CoordinatorFactory
}

type ConcreteDriver struct {
	mu             sync.Mutex
	owner          *RuntimeOwner
	startStdio     StdioStarter
	httpFactory    *remote.Factory
	newCoordinator CoordinatorFactory
	handles        map[CandidateKey]*concreteHandle
}

type concreteHandle struct {
	mu             sync.Mutex
	transport      downstream.Transport
	coordinator    *downstream.Coordinator
	stop           func(context.Context) bool
	closeAttempted bool
	authorization  string
}

func NewConcreteDriver(options ConcreteDriverOptions) (*ConcreteDriver, error) {
	if options.Owner == nil || options.StartStdio == nil || options.HTTPFactory == nil {
		return nil, errors.New("concrete runtime driver dependencies are incomplete")
	}
	if options.NewCoordinator == nil {
		options.NewCoordinator = downstream.NewCoordinator
	}
	return &ConcreteDriver{owner: options.Owner, startStdio: options.StartStdio, httpFactory: options.HTTPFactory, newCoordinator: options.NewCoordinator, handles: make(map[CandidateKey]*concreteHandle)}, nil
}

func (driver *ConcreteDriver) Reconcile(ctx context.Context, candidate Candidate, lease *MaterialLease) Outcome {
	key, err := driver.owner.Admit(candidate, lease, func(OwnedRuntime) error {
		return driver.construct(ctx, candidate)
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
	reason := contract.ReasonProtocolUnsupported
	return Outcome{State: contract.RuntimeDegraded, CredentialState: contract.ServerCredentialReady, CatalogState: contract.ActiveCatalogAbsent, Reason: &reason}
}

func (driver *ConcreteDriver) construct(ctx context.Context, candidate Candidate) error {
	decoded, err := servers.DecodeTransport(candidate.Server.Transport)
	if err != nil {
		return err
	}
	switch transport := decoded.(type) {
	case contract.StdioTransport:
		return driver.constructStdio(ctx, candidate, transport)
	case contract.StreamableHTTPTransport:
		return driver.constructHTTP(candidate, transport)
	default:
		return servers.ErrInvalidInput
	}
}

func (driver *ConcreteDriver) constructStdio(ctx context.Context, candidate Candidate, desired contract.StdioTransport) error {
	secrets := make(map[string]string)
	if len(desired.SecretEnvironment) != 0 {
		material, ok := driver.owner.Material(candidate.Key(), contract.ServerCredentialStatic)
		if !ok {
			return ErrMaterialLease
		}
		generation, err := servercredentials.DecodeStaticGeneration(material)
		if err != nil {
			return err
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
		return err
	}
	transport, err := downstream.NewStdioTransport(process)
	if err != nil {
		if !process.Stop(ctx) {
			driver.storeHandle(candidate.Key(), &concreteHandle{stop: process.Stop})
		}
		return err
	}
	return driver.installTransport(candidate.Key(), transport, "", process.Stop)
}

func (driver *ConcreteDriver) constructHTTP(candidate Candidate, desired contract.StreamableHTTPTransport) error {
	authorization := ""
	allowLoopbackHTTP := false
	switch desired.Authentication.(type) {
	case contract.NoAuthentication:
		allowLoopbackHTTP = true
	case contract.BearerAuthentication:
		material, ok := driver.owner.Material(candidate.Key(), contract.ServerCredentialStatic)
		if !ok {
			return ErrMaterialLease
		}
		generation, err := servercredentials.DecodeStaticGeneration(material)
		if err != nil || generation.Values["bearer"] == "" {
			return ErrMaterialLease
		}
		authorization = "Bearer " + generation.Values["bearer"]
	case contract.OAuthAuthentication:
		accessToken, ok := driver.owner.Material(candidate.Key(), contract.ServerCredentialOAuthTokens)
		if !ok || len(accessToken) == 0 {
			return ErrMaterialLease
		}
		authorization = "Bearer " + string(accessToken)
	default:
		return servers.ErrInvalidInput
	}
	endpoint, err := remote.ParseEndpoint(desired.URL, allowLoopbackHTTP)
	if err != nil {
		return err
	}
	transport, err := downstream.NewHTTPTransport(driver.httpFactory, endpoint, authorization)
	if err != nil {
		return err
	}
	return driver.installTransport(candidate.Key(), transport, authorization, func(ctx context.Context) bool { return transport.Close(ctx) == nil })
}

func (driver *ConcreteDriver) installTransport(key CandidateKey, transport downstream.Transport, authorization string, stop func(context.Context) bool) error {
	handle := &concreteHandle{transport: transport, stop: stop, authorization: authorization}
	if !driver.storeHandle(key, handle) {
		_ = transport.Close(context.Background())
		return ErrCandidateOwned
	}
	coordinator, err := driver.newCoordinator(transport)
	if err != nil {
		return err
	}
	handle.mu.Lock()
	handle.coordinator = coordinator
	handle.mu.Unlock()
	return nil
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
	verified := false
	switch {
	case !handle.closeAttempted && handle.coordinator != nil:
		handle.closeAttempted = true
		verified = handle.coordinator.Close(ctx) == nil
	case handle.stop != nil:
		verified = handle.stop(ctx)
	case handle.transport != nil:
		verified = handle.transport.Close(ctx) == nil
	}
	if !verified {
		return false
	}
	driver.mu.Lock()
	if driver.handles[key] == handle {
		delete(driver.handles, key)
	}
	driver.mu.Unlock()
	return true
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
	reason := contract.ReasonConnectivity
	retry := true
	if errors.Is(err, ErrRuntimeOwnerLimit) {
		reason = contract.ReasonResourceLimit
	} else if errors.Is(err, ErrMaterialLease) || errors.Is(err, servers.ErrInvalidInput) || errors.Is(err, remote.ErrInvalidURL) {
		reason = contract.ReasonConfigurationInvalid
		retry = false
	}
	return Outcome{State: contract.RuntimeDegraded, CredentialState: contract.ServerCredentialUnavailable, CatalogState: contract.ActiveCatalogAbsent, Reason: &reason, Retryable: retry}
}

func cloneStrings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
