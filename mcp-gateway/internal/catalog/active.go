package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type Publication struct {
	Fence             CommitFence
	RuntimeID         string
	RuntimeGeneration uint64
	Candidate         NormalizedCandidate
	Current           func() bool
}

type ActiveStatus struct {
	State      contract.ActiveCatalogState
	Revision   *string
	ToolCount  int64
	IssueCount int64
}

type ActiveCursor struct {
	Generation string `json:"generation"`
	Upper      int64  `json:"upper"`
	After      int64  `json:"after"`
	AfterID    string `json:"after_id"`
}

type ActivePage struct {
	Summary contract.CatalogSummary
	Items   []DescriptorRecord
	Next    *ActiveCursor
}

type ActiveTool struct {
	Record   DescriptorRecord
	Bindings []HeaderBinding
}

type activeServerSnapshot struct {
	RuntimeID         string
	RuntimeGeneration uint64
	State             contract.ActiveCatalogState
	Revision          *string
	IssueCount        int64
	Tools             []ActiveTool
}

type ActiveRegistry struct {
	repository *Repository
	clock      Clock
	processID  string

	mu        sync.RWMutex
	counter   uint64
	changedAt *string
	servers   map[string]activeServerSnapshot
}

func NewActiveRegistry(repository *Repository, clock Clock, processID string) (*ActiveRegistry, error) {
	if repository == nil || clock == nil || processID == "" {
		return nil, errors.New("active catalog repository, clock, and process ID are required")
	}
	return &ActiveRegistry{repository: repository, clock: clock, processID: processID, servers: make(map[string]activeServerSnapshot)}, nil
}

func (registry *ActiveRegistry) Publish(ctx context.Context, publication Publication) (ActiveStatus, error) {
	if publication.RuntimeID == "" || publication.Current == nil || publication.Fence.ServerID == "" {
		return ActiveStatus{}, servers.ErrInvalidInput
	}
	if len(publication.Candidate.Tools) > int(fixedLimit("active_tools_per_server")) {
		return ActiveStatus{}, servers.ErrResourceLimit
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	projected := registry.activeToolCountLocked() - int64(len(registry.servers[publication.Fence.ServerID].Tools)) + int64(len(publication.Candidate.Tools))
	if projected > fixedLimit("active_tools") {
		return ActiveStatus{}, servers.ErrResourceLimit
	}
	if !publication.Current() {
		return ActiveStatus{}, servers.ErrStaleRevision
	}
	durable, err := registry.repository.Commit(ctx, publication.Fence, publication.Candidate)
	if err != nil {
		return ActiveStatus{}, err
	}
	if !publication.Current() {
		registry.withdrawLocked(publication.Fence.ServerID, publication.RuntimeID, contract.ActiveCatalogAbsent)
		return ActiveStatus{}, servers.ErrStaleRevision
	}
	records, err := registry.currentDescriptors(ctx, publication.Fence.ServerID)
	if err != nil {
		registry.withdrawLocked(publication.Fence.ServerID, publication.RuntimeID, contract.ActiveCatalogUnavailable)
		return ActiveStatus{}, err
	}
	bindings := make(map[string][]HeaderBinding, len(publication.Candidate.Tools))
	for _, tool := range publication.Candidate.Tools {
		bindings[tool.Key.UpstreamName] = cloneBindings(tool.HeaderBindings)
	}
	tools := make([]ActiveTool, 0, len(records))
	for _, record := range records {
		tools = append(tools, ActiveTool{Record: cloneDescriptorRecord(record), Bindings: bindings[record.Resource.UpstreamName]})
	}
	snapshot := activeServerSnapshot{
		RuntimeID: publication.RuntimeID, RuntimeGeneration: publication.RuntimeGeneration,
		State: contract.ActiveCatalogCurrent, Revision: cloneActiveString(durable.Revision),
		IssueCount: durable.IssueCount, Tools: tools,
	}
	registry.servers[publication.Fence.ServerID] = snapshot
	registry.advanceLocked()
	return activeStatus(snapshot), nil
}

func (registry *ActiveRegistry) MarkStale(serverID, runtimeID string, issueCount int64) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, ok := registry.servers[serverID]
	if !ok || current.RuntimeID != runtimeID || current.State == contract.ActiveCatalogAbsent || current.State == contract.ActiveCatalogUnavailable {
		return false
	}
	if current.State == contract.ActiveCatalogStale && current.IssueCount == issueCount {
		return true
	}
	current.State = contract.ActiveCatalogStale
	current.IssueCount = issueCount
	registry.servers[serverID] = current
	registry.advanceLocked()
	return true
}

func (registry *ActiveRegistry) Withdraw(serverID, runtimeID string, state contract.ActiveCatalogState) bool {
	if state != contract.ActiveCatalogAbsent && state != contract.ActiveCatalogUnavailable {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.withdrawLocked(serverID, runtimeID, state)
}

func (registry *ActiveRegistry) MarkUnavailable(serverID, runtimeID string, issueCount int64) bool {
	if runtimeID == "" || issueCount < 0 {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, ok := registry.servers[serverID]
	if ok && current.RuntimeID != runtimeID {
		return false
	}
	if current.State == contract.ActiveCatalogUnavailable && current.Revision == nil && len(current.Tools) == 0 && current.IssueCount == issueCount {
		return true
	}
	current.RuntimeID = runtimeID
	current.State = contract.ActiveCatalogUnavailable
	current.Revision = nil
	current.Tools = nil
	current.IssueCount = issueCount
	registry.servers[serverID] = current
	registry.advanceLocked()
	return true
}

func (registry *ActiveRegistry) Status(serverID string) ActiveStatus {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	current, ok := registry.servers[serverID]
	if !ok {
		return ActiveStatus{State: contract.ActiveCatalogAbsent}
	}
	return activeStatus(current)
}

func (registry *ActiveRegistry) Summary() contract.CatalogSummary {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.summaryLocked()
}

func (registry *ActiveRegistry) Occupancy() contract.LimitStatus {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	count := registry.activeToolCountLocked()
	limit := fixedLimit("active_tools")
	return contract.LimitStatus{InUse: count, Limit: limit, Saturated: count >= limit}
}

func (registry *ActiveRegistry) List(cursor *ActiveCursor, limit int) (ActivePage, error) {
	if limit < 1 || limit > int(fixedLimit("s2_list_page")) {
		return ActivePage{}, servers.ErrInvalidInput
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	generation := registry.generationLocked()
	if cursor != nil && cursor.Generation != generation {
		return ActivePage{}, servers.ErrStaleCursor
	}
	items := make([]DescriptorRecord, 0, registry.activeToolCountLocked())
	for _, snapshot := range registry.servers {
		if snapshot.State == contract.ActiveCatalogAbsent || snapshot.State == contract.ActiveCatalogUnavailable {
			continue
		}
		for _, tool := range snapshot.Tools {
			items = append(items, cloneDescriptorRecord(tool.Record))
		}
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].InsertionSequence != items[right].InsertionSequence {
			return items[left].InsertionSequence < items[right].InsertionSequence
		}
		return items[left].Resource.ID < items[right].Resource.ID
	})
	upper := int64(0)
	if cursor != nil {
		upper = cursor.Upper
	} else if len(items) != 0 {
		upper = items[len(items)-1].InsertionSequence
	}
	filtered := make([]DescriptorRecord, 0, len(items))
	for _, item := range items {
		if item.InsertionSequence > upper || cursor != nil && (item.InsertionSequence < cursor.After || item.InsertionSequence == cursor.After && item.Resource.ID <= cursor.AfterID) {
			continue
		}
		filtered = append(filtered, item)
	}
	page := ActivePage{Summary: registry.summaryLocked(), Items: filtered}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &ActiveCursor{Generation: generation, Upper: upper, After: last.InsertionSequence, AfterID: last.Resource.ID}
	}
	return page, nil
}

func (registry *ActiveRegistry) currentDescriptors(ctx context.Context, serverID string) ([]DescriptorRecord, error) {
	var result []DescriptorRecord
	var cursor *DescriptorCursor
	for {
		page, err := registry.repository.ListDescriptors(ctx, serverID, contract.DescriptorRetiredExclude, cursor, int(fixedLimit("s2_list_page")))
		if err != nil {
			return nil, err
		}
		result = append(result, page.Items...)
		if page.Next == nil {
			return result, nil
		}
		cursor = page.Next
	}
}

func (registry *ActiveRegistry) withdrawLocked(serverID, runtimeID string, state contract.ActiveCatalogState) bool {
	current, ok := registry.servers[serverID]
	if !ok {
		if state != contract.ActiveCatalogUnavailable || runtimeID == "" {
			return false
		}
		registry.servers[serverID] = activeServerSnapshot{RuntimeID: runtimeID, State: state}
		registry.advanceLocked()
		return true
	}
	if current.RuntimeID != runtimeID {
		return false
	}
	if current.State == state && len(current.Tools) == 0 && current.Revision == nil {
		return true
	}
	current.State = state
	current.Revision = nil
	current.Tools = nil
	registry.servers[serverID] = current
	registry.advanceLocked()
	return true
}

func (registry *ActiveRegistry) advanceLocked() {
	registry.counter++
	changed := registry.clock.Now().UTC().Format(time.RFC3339Nano)
	registry.changedAt = &changed
}

func (registry *ActiveRegistry) generationLocked() string {
	return fmt.Sprintf("%s-%d", registry.processID, registry.counter)
}

func (registry *ActiveRegistry) summaryLocked() contract.CatalogSummary {
	state := contract.AggregateCatalogEmpty
	count := registry.activeToolCountLocked()
	degraded := false
	issues := int64(0)
	for _, snapshot := range registry.servers {
		issues += snapshot.IssueCount
		degraded = degraded || snapshot.State == contract.ActiveCatalogStale || snapshot.State == contract.ActiveCatalogUnavailable
	}
	if count > 0 {
		state = contract.AggregateCatalogCurrent
		if degraded {
			state = contract.AggregateCatalogDegraded
		}
	}
	return contract.CatalogSummary{ActiveState: state, ActiveGeneration: registry.generationLocked(), ChangedAt: cloneActiveString(registry.changedAt), IssueCount: issues}
}

func (registry *ActiveRegistry) activeToolCountLocked() int64 {
	count := int64(0)
	for _, snapshot := range registry.servers {
		if snapshot.State != contract.ActiveCatalogAbsent && snapshot.State != contract.ActiveCatalogUnavailable {
			count += int64(len(snapshot.Tools))
		}
	}
	return count
}

func activeStatus(snapshot activeServerSnapshot) ActiveStatus {
	count := int64(0)
	if snapshot.State != contract.ActiveCatalogAbsent && snapshot.State != contract.ActiveCatalogUnavailable {
		count = int64(len(snapshot.Tools))
	}
	return ActiveStatus{State: snapshot.State, Revision: cloneActiveString(snapshot.Revision), ToolCount: count, IssueCount: snapshot.IssueCount}
}

func cloneDescriptorRecord(source DescriptorRecord) DescriptorRecord {
	result := source
	result.Resource.Descriptor.InputSchema = append([]byte(nil), source.Resource.Descriptor.InputSchema...)
	result.Resource.Descriptor.OutputSchema = append([]byte(nil), source.Resource.Descriptor.OutputSchema...)
	result.Resource.Descriptor.Title = cloneActiveString(source.Resource.Descriptor.Title)
	result.Resource.Descriptor.Description = cloneActiveString(source.Resource.Descriptor.Description)
	result.Resource.Descriptor.Annotations.Title = cloneActiveString(source.Resource.Descriptor.Annotations.Title)
	result.Resource.RetiredAt = cloneActiveString(source.Resource.RetiredAt)
	return result
}

func cloneBindings(source []HeaderBinding) []HeaderBinding {
	result := make([]HeaderBinding, len(source))
	for index, binding := range source {
		result[index] = binding
		result[index].Path = append([]string(nil), binding.Path...)
	}
	return result
}

func cloneActiveString(source *string) *string {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}
