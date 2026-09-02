package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
)

type CallTarget struct {
	ExternalName          string
	ServerID              string
	ToolID                string
	UpstreamName          string
	DescriptorRevision    string
	DescriptorFingerprint string
	Validator             *InputValidator
	Capability            *downstream.Capability
}

type routeEntry struct {
	serverID          string
	runtimeID         string
	runtimeGeneration uint64
	externalName      string
	target            CallTarget
}

type RouteRegistry struct {
	mu         sync.RWMutex
	dispatcher *downstream.Dispatcher
	active     *ActiveRegistry
	tools      map[string]routeEntry
	names      map[string]routeEntry
}

func newRouteRegistry(active *ActiveRegistry) (*RouteRegistry, error) {
	dispatcher, err := downstream.NewDispatcher()
	if err != nil {
		return nil, err
	}
	return &RouteRegistry{dispatcher: dispatcher, active: active, tools: make(map[string]routeEntry), names: make(map[string]routeEntry)}, nil
}

func (registry *RouteRegistry) Resolve(toolID string) (*downstream.Capability, bool) {
	registry.active.mu.RLock()
	defer registry.active.mu.RUnlock()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	entry, ok := registry.tools[toolID]
	return entry.target.Capability, ok
}

func (registry *RouteRegistry) ResolveCall(externalName string) (CallTarget, bool) {
	registry.active.mu.RLock()
	defer registry.active.mu.RUnlock()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	entry, ok := registry.names[externalName]
	if !ok || registry.active.draining.Load() {
		return CallTarget{}, false
	}
	snapshot, ok := registry.active.servers[entry.serverID]
	if !ok || snapshot.State != contract.ActiveCatalogCurrent {
		return CallTarget{}, false
	}
	return entry.target, true
}

func (registry *RouteRegistry) Status() contract.LimitStatus { return registry.dispatcher.Status() }
func (registry *RouteRegistry) ServerStatus(serverID string) contract.LimitStatus {
	return registry.dispatcher.ServerStatus(serverID)
}

func (registry *RouteRegistry) replace(publication Publication, tools []ActiveTool, revision string) ([]*downstream.Capability, error) {
	prepared := make(map[string]routeEntry, len(tools))
	if publication.Runtime != nil {
		for _, tool := range tools {
			binding := downstream.Binding{ServerID: publication.Fence.ServerID, ToolID: tool.Record.Resource.ID, UpstreamName: tool.Record.Resource.UpstreamName, RuntimeID: publication.RuntimeID, RuntimeGeneration: publication.RuntimeGeneration, DesiredRevision: publication.Fence.ExpectedDesiredRevision, CredentialRevisions: publication.Fence.ExpectedCredentialRevisions, CatalogRevision: revision}
			bindings := cloneBindings(tool.Bindings)
			capability, err := registry.dispatcher.Publish(binding, publication.Runtime, registry.active.revalidate, func(arguments json.RawMessage) (map[string]string, error) {
				values, err := decodeValidatedArguments(arguments)
				if err != nil {
					return nil, err
				}
				return MirrorHeaders(bindings, values)
			})
			if err != nil {
				return nil, err
			}
			target := CallTarget{
				ExternalName: tool.Record.Resource.ExternalName, ServerID: binding.ServerID, ToolID: binding.ToolID,
				UpstreamName: binding.UpstreamName, DescriptorRevision: tool.Record.Resource.CatalogRevision,
				DescriptorFingerprint: tool.Record.Resource.Fingerprint, Validator: tool.validator, Capability: capability,
			}
			prepared[binding.ToolID] = routeEntry{
				serverID: binding.ServerID, runtimeID: binding.RuntimeID, runtimeGeneration: binding.RuntimeGeneration,
				externalName: target.ExternalName, target: target,
			}
		}
	}
	registry.mu.Lock()
	old := registry.removeServerLocked(publication.Fence.ServerID, "", 0)
	for toolID, entry := range prepared {
		registry.tools[toolID] = entry
		registry.names[entry.externalName] = entry
	}
	registry.mu.Unlock()
	return old, nil
}

func (registry *RouteRegistry) withdraw(serverID, runtimeID string, runtimeGeneration uint64) []*downstream.Capability {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.removeServerLocked(serverID, runtimeID, runtimeGeneration)
}

func (registry *RouteRegistry) withdrawAll() []*downstream.Capability {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	result := make([]*downstream.Capability, 0, len(registry.tools))
	for toolID, entry := range registry.tools {
		entry.target.Capability.Fence()
		result = append(result, entry.target.Capability)
		delete(registry.names, entry.externalName)
		delete(registry.tools, toolID)
	}
	return result
}

func (registry *RouteRegistry) removeServerLocked(serverID, runtimeID string, runtimeGeneration uint64) []*downstream.Capability {
	result := make([]*downstream.Capability, 0)
	for toolID, entry := range registry.tools {
		if entry.serverID != serverID || runtimeID != "" && entry.runtimeID != runtimeID || runtimeGeneration != 0 && entry.runtimeGeneration != runtimeGeneration {
			continue
		}
		entry.target.Capability.Fence()
		result = append(result, entry.target.Capability)
		delete(registry.names, entry.externalName)
		delete(registry.tools, toolID)
	}
	return result
}

func decodeValidatedArguments(arguments json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil, downstream.ErrInvalidMessage
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, downstream.ErrInvalidMessage
	}
	return values, nil
}

func withdrawCapabilities(ctx context.Context, capabilities []*downstream.Capability) {
	for _, capability := range capabilities {
		_ = capability.Withdraw(ctx)
	}
}
