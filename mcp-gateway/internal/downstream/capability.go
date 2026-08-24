package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

var (
	ErrCapabilitySerialization = errors.New("runtime capability cannot be serialized")
	ErrLeaseConsumed           = errors.New("runtime capability lease is already consumed")
	ErrLeaseCancelled          = errors.New("runtime capability lease is cancelled")
)

type Availability string

const (
	Current     Availability = "current"
	Stale       Availability = "stale"
	Draining    Availability = "draining"
	Unavailable Availability = "unavailable"
)

type RejectionReason string

const (
	RejectionGlobalSaturated RejectionReason = "global_saturated"
	RejectionServerSaturated RejectionReason = "server_saturated"
	RejectionStale           RejectionReason = "stale"
	RejectionWithdrawn       RejectionReason = "withdrawn"
	RejectionDraining        RejectionReason = "draining"
	RejectionUnavailable     RejectionReason = "unavailable"
	RejectionCancelled       RejectionReason = "cancelled"
)

type PreStartRejection struct {
	Reason  RejectionReason
	Failure FailureClass
}

func (rejection *PreStartRejection) Error() string {
	return "downstream capability rejected before start: " + string(rejection.Reason)
}

type Binding struct {
	ServerID            string
	ToolID              string
	UpstreamName        string
	RuntimeID           string
	DesiredRevision     string
	CredentialRevisions contract.CredentialRevisions
	CatalogRevision     string
}

type Revalidate func(context.Context, Binding) Availability
type ParameterHeaderProvider func(json.RawMessage) (map[string]string, error)

type Dispatcher struct {
	mu              sync.Mutex
	global          chan struct{}
	serverLimit     int
	servers         map[string]*serverAdmission
	releaseObserver func(string)
}

type serverAdmission struct {
	slots chan struct{}
	users int
}

type Capability struct {
	mu         sync.Mutex
	dispatcher *Dispatcher
	binding    Binding
	runtime    *Runtime
	revalidate Revalidate
	headers    ParameterHeaderProvider
	leases     map[*Lease]struct{}
	current    bool
}

type Lease struct {
	mu         sync.Mutex
	capability *Capability
	server     *serverAdmission
	used       bool
	cancelled  bool
	released   bool
	call       *Call
}

func NewDispatcher() (*Dispatcher, error) {
	global, ok := contract.FixedLimitByName("downstream_dispatch")
	if !ok || global.Maximum < 1 {
		return nil, ErrInvalidMessage
	}
	perServer, ok := contract.FixedLimitByName("per_server_downstream_dispatch")
	if !ok || perServer.Maximum < 1 {
		return nil, ErrInvalidMessage
	}
	return &Dispatcher{global: make(chan struct{}, int(global.Maximum)), serverLimit: int(perServer.Maximum), servers: make(map[string]*serverAdmission)}, nil
}

func (dispatcher *Dispatcher) Publish(binding Binding, runtime *Runtime, revalidate Revalidate, providers ...ParameterHeaderProvider) (*Capability, error) {
	if !validBinding(binding) || runtime == nil || revalidate == nil || len(providers) > 1 || len(providers) == 1 && providers[0] == nil {
		return nil, ErrInvalidMessage
	}
	var headers ParameterHeaderProvider
	if len(providers) == 1 {
		headers = providers[0]
	}
	return &Capability{dispatcher: dispatcher, binding: binding, runtime: runtime, revalidate: revalidate, headers: headers, leases: make(map[*Lease]struct{}), current: true}, nil
}

func (dispatcher *Dispatcher) Status() contract.LimitStatus {
	inUse := len(dispatcher.global)
	limit := cap(dispatcher.global)
	return contract.LimitStatus{InUse: int64(inUse), Limit: int64(limit), Saturated: inUse == limit}
}

func (dispatcher *Dispatcher) ServerStatus(serverID string) contract.LimitStatus {
	dispatcher.mu.Lock()
	server := dispatcher.servers[serverID]
	dispatcher.mu.Unlock()
	inUse := 0
	if server != nil {
		inUse = len(server.slots)
	}
	return contract.LimitStatus{InUse: int64(inUse), Limit: int64(dispatcher.serverLimit), Saturated: inUse == dispatcher.serverLimit}
}

func (dispatcher *Dispatcher) acquireServer(serverID string) *serverAdmission {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	server := dispatcher.servers[serverID]
	if server == nil {
		server = &serverAdmission{slots: make(chan struct{}, dispatcher.serverLimit)}
		dispatcher.servers[serverID] = server
	}
	server.users++
	return server
}

func (dispatcher *Dispatcher) releaseServer(serverID string, server *serverAdmission) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	server.users--
	if server.users == 0 && len(server.slots) == 0 && dispatcher.servers[serverID] == server {
		delete(dispatcher.servers, serverID)
	}
}

func (capability *Capability) Acquire(ctx context.Context) (*Lease, error) {
	if ctx.Err() != nil {
		return nil, reject(RejectionCancelled)
	}
	capability.mu.Lock()
	current := capability.current
	capability.mu.Unlock()
	if !current {
		return nil, reject(RejectionWithdrawn)
	}
	select {
	case capability.dispatcher.global <- struct{}{}:
	default:
		return nil, reject(RejectionGlobalSaturated)
	}
	server := capability.dispatcher.acquireServer(capability.binding.ServerID)
	select {
	case server.slots <- struct{}{}:
	default:
		capability.dispatcher.releaseServer(capability.binding.ServerID, server)
		<-capability.dispatcher.global
		return nil, reject(RejectionServerSaturated)
	}
	release := func() {
		<-server.slots
		capability.dispatcher.releaseServer(capability.binding.ServerID, server)
		<-capability.dispatcher.global
	}
	if ctx.Err() != nil {
		release()
		return nil, reject(RejectionCancelled)
	}
	availability := capability.revalidate(ctx, capability.binding)
	if ctx.Err() != nil {
		release()
		return nil, reject(RejectionCancelled)
	}
	if availability != Current {
		release()
		return nil, reject(availabilityReason(availability))
	}
	capability.mu.Lock()
	if !capability.current {
		capability.mu.Unlock()
		release()
		return nil, reject(RejectionWithdrawn)
	}
	lease := &Lease{capability: capability, server: server}
	capability.leases[lease] = struct{}{}
	capability.mu.Unlock()
	return lease, nil
}

func (capability *Capability) Fence() {
	capability.mu.Lock()
	capability.current = false
	capability.mu.Unlock()
}

func (capability *Capability) Withdraw(ctx context.Context) error {
	capability.mu.Lock()
	capability.current = false
	leases := make([]*Lease, 0, len(capability.leases))
	for lease := range capability.leases {
		leases = append(leases, lease)
	}
	capability.mu.Unlock()
	var firstErr error
	for _, lease := range leases {
		if err := lease.Cancel(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (*Capability) MarshalJSON() ([]byte, error) {
	return nil, ErrCapabilitySerialization
}

func (lease *Lease) Execute(ctx context.Context, validatedArguments json.RawMessage) CallResult {
	lease.mu.Lock()
	if lease.used {
		lease.mu.Unlock()
		return CallResult{Failure: FailurePreStart, Err: ErrLeaseConsumed}
	}
	lease.used = true
	if lease.cancelled || ctx.Err() != nil {
		lease.mu.Unlock()
		lease.release()
		return CallResult{Failure: FailurePreStart, Err: ErrLeaseCancelled}
	}
	lease.mu.Unlock()
	capability := lease.capability
	if availability := capability.revalidate(ctx, capability.binding); availability != Current {
		lease.release()
		return CallResult{Failure: FailurePreStart, Err: reject(availabilityReason(availability))}
	}
	capability.mu.Lock()
	current := capability.current
	headers := capability.headers
	capability.mu.Unlock()
	if !current {
		lease.release()
		return CallResult{Failure: FailurePreStart, Err: reject(RejectionWithdrawn)}
	}
	var parameterHeaders map[string]string
	var err error
	if headers != nil {
		parameterHeaders, err = headers(validatedArguments)
		if err != nil {
			lease.release()
			return CallResult{Failure: FailurePreStart, Err: err}
		}
	}
	call, err := capability.runtime.NewCallWithHeaders(capability.binding.UpstreamName, validatedArguments, parameterHeaders)
	if err != nil {
		lease.release()
		return CallResult{Failure: FailurePreStart, Err: err}
	}
	lease.mu.Lock()
	if lease.cancelled {
		lease.mu.Unlock()
		_ = call.Cancel(ctx)
		lease.release()
		return CallResult{Failure: FailurePreStart, Err: ErrLeaseCancelled}
	}
	lease.call = call
	lease.mu.Unlock()
	result := call.Execute(ctx)
	lease.release()
	return result
}

func (lease *Lease) Cancel(ctx context.Context) error {
	lease.mu.Lock()
	if lease.cancelled {
		lease.mu.Unlock()
		return nil
	}
	lease.cancelled = true
	call := lease.call
	used := lease.used
	lease.mu.Unlock()
	if call != nil {
		return call.Cancel(ctx)
	}
	if !used {
		lease.release()
	}
	return nil
}

func (lease *Lease) release() {
	lease.mu.Lock()
	if lease.released {
		lease.mu.Unlock()
		return
	}
	lease.released = true
	server := lease.server
	capability := lease.capability
	lease.mu.Unlock()
	capability.mu.Lock()
	delete(capability.leases, lease)
	capability.mu.Unlock()
	<-server.slots
	capability.dispatcher.releaseServer(capability.binding.ServerID, server)
	if capability.dispatcher.releaseObserver != nil {
		capability.dispatcher.releaseObserver("server")
	}
	<-capability.dispatcher.global
	if capability.dispatcher.releaseObserver != nil {
		capability.dispatcher.releaseObserver("global")
	}
}

func (*Lease) MarshalJSON() ([]byte, error) {
	return nil, ErrCapabilitySerialization
}

func validBinding(binding Binding) bool {
	return binding.ServerID != "" && binding.ToolID != "" && binding.RuntimeID != "" && binding.DesiredRevision != "" && binding.CredentialRevisions.StaticCredential != "" && binding.CredentialRevisions.OAuthClient != "" && binding.CredentialRevisions.OAuthTokens != "" && binding.CatalogRevision != "" && binding.UpstreamName != "" && utf8.ValidString(binding.UpstreamName) && int64(len(binding.UpstreamName)) <= limit("external_tool_name_bytes")
}

func availabilityReason(availability Availability) RejectionReason {
	switch availability {
	case Stale:
		return RejectionStale
	case Draining:
		return RejectionDraining
	default:
		return RejectionUnavailable
	}
}

func reject(reason RejectionReason) *PreStartRejection {
	return &PreStartRejection{Reason: reason, Failure: FailurePreStart}
}
