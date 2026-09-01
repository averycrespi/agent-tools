package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type Publication struct {
	Fence             CommitFence
	ServerDisplayName string
	RuntimeID         string
	RuntimeGeneration uint64
	Candidate         NormalizedCandidate
	Current           func() bool
	Runtime           *downstream.Runtime
}

type ActiveStatus struct {
	State      contract.ActiveCatalogState
	Revision   *string
	ToolCount  int64
	IssueCount int64
}

type PublicationPhase string

const PublicationPhaseDurableOnly PublicationPhase = "durable_only"

type PublicationFailureCause string

const (
	PublicationFailureStale   PublicationFailureCause = "stale"
	PublicationFailureDrain   PublicationFailureCause = "drain"
	PublicationFailureStorage PublicationFailureCause = "storage"
)

type PublicationFailure struct {
	Phase PublicationPhase
	Cause PublicationFailureCause
	Err   error
}

func (failure *PublicationFailure) Error() string { return failure.Err.Error() }
func (failure *PublicationFailure) Unwrap() error { return failure.Err }

func durableOnlyFailure(cause PublicationFailureCause, err error) error {
	return &PublicationFailure{Phase: PublicationPhaseDurableOnly, Cause: cause, Err: err}
}

type ActiveCursor struct {
	Generation string `json:"generation"`
	Upper      int64  `json:"upper"`
	After      int64  `json:"after"`
	AfterID    string `json:"after_id"`
}

type ActivePage struct {
	Summary            contract.CatalogSummary
	Items              []DescriptorRecord
	ServerDisplayNames map[string]string
	Next               *ActiveCursor
}

type CurrentGeneration struct {
	ProcessGeneration string
	ActiveGeneration  uint64
}

type CurrentSnapshot struct {
	Generation  CurrentGeneration
	Descriptors []contract.ToolDescriptor
}

type ActiveTool struct {
	Record    DescriptorRecord
	Bindings  []HeaderBinding
	validator *InputValidator
}

type activeServerSnapshot struct {
	ServerDisplayName   string
	RuntimeID           string
	RuntimeGeneration   uint64
	DesiredRevision     string
	CredentialRevisions contract.CredentialRevisions
	State               contract.ActiveCatalogState
	Revision            *string
	IssueCount          int64
	Tools               []ActiveTool
}

type ActiveRegistry struct {
	repository *Repository
	clock      Clock
	processID  string

	mu                   sync.RWMutex
	counter              uint64
	changedAt            *string
	servers              map[string]activeServerSnapshot
	routes               *RouteRegistry
	draining             atomic.Bool
	afterCommit          func()
	beforeDescriptorRead func() error
	beforeInstall        func()
}

func NewActiveRegistry(repository *Repository, clock Clock, processID string) (*ActiveRegistry, error) {
	if repository == nil || clock == nil || processID == "" {
		return nil, errors.New("active catalog repository, clock, and process ID are required")
	}
	registry := &ActiveRegistry{repository: repository, clock: clock, processID: processID, servers: make(map[string]activeServerSnapshot)}
	routes, err := newRouteRegistry(registry)
	if err != nil {
		return nil, err
	}
	registry.routes = routes
	return registry, nil
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
	if registry.draining.Load() {
		return ActiveStatus{}, servers.ErrStaleRevision
	}
	projected := registry.activeToolCountLocked() - int64(len(registry.servers[publication.Fence.ServerID].Tools)) + int64(len(publication.Candidate.Tools))
	if projected > fixedLimit("active_tools") {
		return ActiveStatus{}, servers.ErrResourceLimit
	}
	if !publication.Current() {
		return ActiveStatus{}, servers.ErrStaleRevision
	}
	if registry.externalNameCollisionLocked(publication.Fence.ServerID, publication.Candidate.Tools) {
		return ActiveStatus{}, servers.ErrInvalidInput
	}
	if err := registry.repository.projectCommit(ctx, publication.Fence, publication.Candidate); err != nil {
		return ActiveStatus{}, err
	}
	durable, err := registry.repository.Commit(ctx, publication.Fence, publication.Candidate)
	if err != nil {
		return ActiveStatus{}, err
	}
	if registry.afterCommit != nil {
		registry.afterCommit()
	}
	if registry.draining.Load() {
		return ActiveStatus{}, durableOnlyFailure(PublicationFailureDrain, servers.ErrStaleRevision)
	}
	if !publication.Current() {
		return ActiveStatus{}, durableOnlyFailure(PublicationFailureStale, servers.ErrStaleRevision)
	}
	if registry.beforeDescriptorRead != nil {
		if err := registry.beforeDescriptorRead(); err != nil {
			return ActiveStatus{}, durableOnlyFailure(PublicationFailureStorage, err)
		}
	}
	records, err := registry.currentDescriptors(ctx, publication.Fence.ServerID)
	if err != nil {
		return ActiveStatus{}, durableOnlyFailure(PublicationFailureStorage, err)
	}
	bindings := make(map[string][]HeaderBinding, len(publication.Candidate.Tools))
	for _, tool := range publication.Candidate.Tools {
		bindings[tool.Key.UpstreamName] = cloneBindings(tool.HeaderBindings)
	}
	tools := make([]ActiveTool, 0, len(records))
	for _, record := range records {
		validator, err := compileInputValidator(record.Resource.Descriptor.InputSchema)
		if err != nil {
			return ActiveStatus{}, durableOnlyFailure(PublicationFailureStorage, err)
		}
		tools = append(tools, ActiveTool{Record: cloneDescriptorRecord(record), Bindings: bindings[record.Resource.UpstreamName], validator: validator})
	}
	revision := ""
	if durable.Revision != nil {
		revision = *durable.Revision
	}
	if registry.beforeInstall != nil {
		registry.beforeInstall()
	}
	if registry.draining.Load() {
		return ActiveStatus{}, durableOnlyFailure(PublicationFailureDrain, servers.ErrStaleRevision)
	}
	if !publication.Current() {
		return ActiveStatus{}, durableOnlyFailure(PublicationFailureStale, servers.ErrStaleRevision)
	}
	oldRoutes, err := registry.routes.replace(publication, tools, revision)
	if err != nil {
		return ActiveStatus{}, durableOnlyFailure(PublicationFailureStorage, err)
	}
	snapshot := activeServerSnapshot{
		ServerDisplayName: publication.ServerDisplayName,
		RuntimeID:         publication.RuntimeID, RuntimeGeneration: publication.RuntimeGeneration,
		DesiredRevision: publication.Fence.ExpectedDesiredRevision, CredentialRevisions: publication.Fence.ExpectedCredentialRevisions,
		State: contract.ActiveCatalogCurrent, Revision: cloneActiveString(durable.Revision),
		IssueCount: durable.IssueCount, Tools: tools,
	}
	registry.servers[publication.Fence.ServerID] = snapshot
	registry.advanceLocked()
	go withdrawCapabilities(context.WithoutCancel(ctx), oldRoutes)
	return activeStatus(snapshot), nil
}

func (registry *ActiveRegistry) externalNameCollisionLocked(serverID string, tools []NormalizedTool) bool {
	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if _, duplicate := names[tool.ExternalName]; duplicate {
			return true
		}
		names[tool.ExternalName] = struct{}{}
	}
	for currentServerID, snapshot := range registry.servers {
		if currentServerID == serverID {
			continue
		}
		for _, tool := range snapshot.Tools {
			if _, collision := names[tool.Record.Resource.ExternalName]; collision {
				return true
			}
		}
	}
	return false
}

func (registry *ActiveRegistry) MarkStale(serverID, runtimeID string, issueCount int64) bool {
	return registry.MarkStaleExact(serverID, runtimeID, 0, issueCount)
}

func (registry *ActiveRegistry) MarkStaleExact(serverID, runtimeID string, runtimeGeneration uint64, issueCount int64) bool {
	if registry.draining.Load() {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.draining.Load() {
		return false
	}
	current, ok := registry.servers[serverID]
	if !ok || current.RuntimeID != runtimeID || runtimeGeneration != 0 && current.RuntimeGeneration != runtimeGeneration || current.State == contract.ActiveCatalogAbsent || current.State == contract.ActiveCatalogUnavailable {
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
	return registry.WithdrawExact(serverID, runtimeID, 0, state)
}

func (registry *ActiveRegistry) FinalizeLifecycle(ctx context.Context, fence CommitFence, desiredState contract.DesiredServerState, durableState contract.DurableCatalogState, activeState contract.ActiveCatalogState) error {
	if activeState != contract.ActiveCatalogAbsent && activeState != contract.ActiveCatalogUnavailable {
		return servers.ErrInvalidInput
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.draining.Load() {
		return servers.ErrStaleRevision
	}
	status, err := registry.repository.Status(ctx, fence.ServerID)
	if err != nil {
		return err
	}
	fence.ExpectedCatalogRevision = "0"
	if status.Revision != nil {
		fence.ExpectedCatalogRevision = *status.Revision
	}
	if _, err := registry.repository.SetLifecycleState(ctx, fence, desiredState, durableState); err != nil {
		return err
	}
	current, ok := registry.servers[fence.ServerID]
	if !ok {
		if activeState == contract.ActiveCatalogAbsent {
			return nil
		}
		return servers.ErrStaleRevision
	}
	if !registry.withdrawLocked(fence.ServerID, current.RuntimeID, current.RuntimeGeneration, activeState) {
		return servers.ErrStaleRevision
	}
	return nil
}

func (registry *ActiveRegistry) WithdrawExact(serverID, runtimeID string, runtimeGeneration uint64, state contract.ActiveCatalogState) bool {
	if registry.draining.Load() || state != contract.ActiveCatalogAbsent && state != contract.ActiveCatalogUnavailable {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.draining.Load() {
		return false
	}
	return registry.withdrawLocked(serverID, runtimeID, runtimeGeneration, state)
}

func (registry *ActiveRegistry) MarkUnavailable(serverID, runtimeID string, issueCount int64) bool {
	return registry.MarkUnavailableExact(serverID, runtimeID, 0, issueCount)
}

func (registry *ActiveRegistry) MarkUnavailableExact(serverID, runtimeID string, runtimeGeneration uint64, issueCount int64) bool {
	if registry.draining.Load() || runtimeID == "" || issueCount < 0 {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.draining.Load() {
		return false
	}
	current, ok := registry.servers[serverID]
	if ok && (current.RuntimeID != runtimeID || runtimeGeneration != 0 && current.RuntimeGeneration != runtimeGeneration) {
		return false
	}
	if current.State == contract.ActiveCatalogUnavailable && current.Revision == nil && len(current.Tools) == 0 && current.IssueCount == issueCount {
		return true
	}
	oldRoutes := registry.routes.withdraw(serverID, runtimeID, runtimeGeneration)
	current.RuntimeID = runtimeID
	if !ok || runtimeGeneration != 0 {
		current.RuntimeGeneration = runtimeGeneration
	}
	current.State = contract.ActiveCatalogUnavailable
	current.Revision = nil
	current.Tools = nil
	current.IssueCount = issueCount
	registry.servers[serverID] = current
	registry.advanceLocked()
	go withdrawCapabilities(context.Background(), oldRoutes)
	return true
}

func (registry *ActiveRegistry) Routes() *RouteRegistry { return registry.routes }

func (registry *ActiveRegistry) Drain() {
	if !registry.draining.CompareAndSwap(false, true) {
		return
	}
	registry.mu.Lock()
	oldRoutes := registry.routes.withdrawAll()
	changed := false
	for serverID, snapshot := range registry.servers {
		if snapshot.State == contract.ActiveCatalogAbsent || snapshot.State == contract.ActiveCatalogUnavailable {
			continue
		}
		snapshot.State = contract.ActiveCatalogUnavailable
		snapshot.Revision = nil
		snapshot.Tools = nil
		registry.servers[serverID] = snapshot
		changed = true
	}
	if changed {
		registry.advanceLocked()
	}
	registry.mu.Unlock()
	withdrawCapabilities(context.Background(), oldRoutes)
}

func (registry *ActiveRegistry) revalidate(_ context.Context, binding downstream.Binding) downstream.Availability {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if registry.draining.Load() {
		return downstream.Draining
	}
	snapshot, ok := registry.servers[binding.ServerID]
	if !ok || snapshot.State == contract.ActiveCatalogAbsent || snapshot.State == contract.ActiveCatalogUnavailable {
		return downstream.Unavailable
	}
	if snapshot.State == contract.ActiveCatalogStale {
		return downstream.Stale
	}
	if snapshot.RuntimeID != binding.RuntimeID || snapshot.RuntimeGeneration != binding.RuntimeGeneration || snapshot.DesiredRevision != binding.DesiredRevision || snapshot.CredentialRevisions != binding.CredentialRevisions || snapshot.Revision == nil || *snapshot.Revision != binding.CatalogRevision {
		return downstream.Stale
	}
	for _, tool := range snapshot.Tools {
		if tool.Record.Resource.ID == binding.ToolID && tool.Record.Resource.UpstreamName == binding.UpstreamName {
			return downstream.Current
		}
	}
	return downstream.Unavailable
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

func (registry *ActiveRegistry) CompareActiveTarget(ctx context.Context, serverID, upstreamName, fingerprint string) contract.TargetActiveState {
	if ctx.Err() != nil || registry.draining.Load() {
		return contract.TargetActiveUnavailable
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if ctx.Err() != nil || registry.draining.Load() {
		return contract.TargetActiveUnavailable
	}
	current, ok := registry.servers[serverID]
	if !ok || current.State == contract.ActiveCatalogAbsent {
		return contract.TargetActiveAbsent
	}
	if current.State == contract.ActiveCatalogUnavailable {
		return contract.TargetActiveUnavailable
	}
	if current.State == contract.ActiveCatalogStale {
		return contract.TargetActiveStale
	}
	for _, tool := range current.Tools {
		if tool.Record.Resource.UpstreamName != upstreamName {
			continue
		}
		if fingerprint == "" || tool.Record.Resource.Fingerprint != fingerprint {
			return contract.TargetActiveStale
		}
		return contract.TargetActiveCurrent
	}
	return contract.TargetActiveAbsent
}

func (registry *ActiveRegistry) Summary() contract.CatalogSummary {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.summaryLocked()
}

func (registry *ActiveRegistry) CurrentSnapshot() CurrentSnapshot {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	descriptors := make([]contract.ToolDescriptor, 0, registry.activeToolCountLocked())
	if !registry.draining.Load() {
		for _, snapshot := range registry.servers {
			if snapshot.State != contract.ActiveCatalogCurrent {
				continue
			}
			for _, tool := range snapshot.Tools {
				descriptors = append(descriptors, cloneDescriptorRecord(tool.Record).Resource)
			}
		}
	}
	sort.Slice(descriptors, func(left, right int) bool {
		if descriptors[left].ExternalName != descriptors[right].ExternalName {
			return descriptors[left].ExternalName < descriptors[right].ExternalName
		}
		return descriptors[left].ID < descriptors[right].ID
	})
	return CurrentSnapshot{
		Generation:  CurrentGeneration{ProcessGeneration: registry.processID, ActiveGeneration: registry.counter},
		Descriptors: descriptors,
	}
}

func (registry *ActiveRegistry) IsCurrentGeneration(generation CurrentGeneration) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return !registry.draining.Load() && generation.ProcessGeneration == registry.processID && generation.ActiveGeneration == registry.counter
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
	serverDisplayNames := make(map[string]string, len(registry.servers))
	for serverID, snapshot := range registry.servers {
		if snapshot.State == contract.ActiveCatalogAbsent || snapshot.State == contract.ActiveCatalogUnavailable {
			continue
		}
		serverDisplayNames[serverID] = snapshot.ServerDisplayName
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
	page := ActivePage{Summary: registry.summaryLocked(), Items: filtered, ServerDisplayNames: serverDisplayNames}
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

func (registry *ActiveRegistry) withdrawLocked(serverID, runtimeID string, runtimeGeneration uint64, state contract.ActiveCatalogState) bool {
	current, ok := registry.servers[serverID]
	if !ok {
		if state != contract.ActiveCatalogUnavailable || runtimeID == "" {
			return false
		}
		registry.servers[serverID] = activeServerSnapshot{RuntimeID: runtimeID, RuntimeGeneration: runtimeGeneration, State: state}
		registry.advanceLocked()
		return true
	}
	if current.RuntimeID != runtimeID || runtimeGeneration != 0 && current.RuntimeGeneration != runtimeGeneration {
		return false
	}
	oldRoutes := registry.routes.withdraw(serverID, runtimeID, runtimeGeneration)
	go withdrawCapabilities(context.Background(), oldRoutes)
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
