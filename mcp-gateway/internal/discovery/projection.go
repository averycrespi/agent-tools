// Package discovery projects coherent principal-specific MCP tool listings from
// SQL authorization policy and the process-local current catalog.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

var (
	ErrStaleCursor              = errors.New("tools/list cursor is stale")
	ErrAuthorizationUnavailable = errors.New("discovery authorization is unavailable")
)

type PolicySource interface {
	LoadDiscoveryPolicy(context.Context, *authorization.Lease, time.Time) (authorization.DiscoveryPolicy, error)
}

type CatalogSource interface {
	CurrentSnapshot() catalog.CurrentSnapshot
	IsCurrentGeneration(catalog.CurrentGeneration) bool
}

type Clock interface {
	Now() time.Time
}

type Snapshot struct {
	Generation            catalog.CurrentGeneration
	PrincipalID           string
	PrincipalRevision     string
	Visibility            contract.PrincipalVisibility
	CredentialID          string
	CredentialRevision    string
	AuthorizationRevision string
	EvaluatedAt           time.Time
}

type Request struct {
	Lease        *authorization.Lease
	Continuation *Snapshot
}

type ToolAnnotations struct {
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	Title           string `json:"title,omitempty"`
}

type Tool struct {
	Annotations  *ToolAnnotations `json:"annotations,omitempty"`
	Description  string           `json:"description,omitempty"`
	InputSchema  json.RawMessage  `json:"inputSchema"`
	Name         string           `json:"name"`
	OutputSchema json.RawMessage  `json:"outputSchema,omitempty"`
	Title        string           `json:"title,omitempty"`
}

type Projection struct {
	Tools    []*Tool
	Snapshot Snapshot
}

type Service struct {
	policy  PolicySource
	catalog CatalogSource
	clock   Clock
}

func New(policy PolicySource, catalogs CatalogSource, clock Clock) (*Service, error) {
	if policy == nil || catalogs == nil || clock == nil {
		return nil, ErrAuthorizationUnavailable
	}
	return &Service{policy: policy, catalog: catalogs, clock: clock}, nil
}

func (service *Service) Project(ctx context.Context, request Request) (Projection, error) {
	if service == nil || service.policy == nil || service.catalog == nil || service.clock == nil {
		return Projection{}, ErrAuthorizationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Projection{}, err
	}
	evaluatedAt := service.clock.Now().UTC()
	if request.Continuation != nil {
		if request.Continuation.EvaluatedAt.IsZero() {
			return Projection{}, ErrStaleCursor
		}
		evaluatedAt = request.Continuation.EvaluatedAt.UTC()
	}
	policy, err := service.policy.LoadDiscoveryPolicy(ctx, request.Lease, evaluatedAt)
	if err != nil {
		return Projection{}, mapPolicyError(err)
	}
	if err := ctx.Err(); err != nil {
		return Projection{}, err
	}
	catalogSnapshot := service.catalog.CurrentSnapshot()
	if err := ctx.Err(); err != nil {
		return Projection{}, err
	}
	resultSnapshot := snapshotFrom(policy, catalogSnapshot.Generation)
	if request.Continuation != nil && !sameSnapshot(*request.Continuation, resultSnapshot) {
		return Projection{}, ErrStaleCursor
	}

	type projectedTool struct {
		id   string
		tool *Tool
	}
	projected := make([]projectedTool, 0, len(catalogSnapshot.Descriptors))
	for _, descriptor := range catalogSnapshot.Descriptors {
		if err := ctx.Err(); err != nil {
			return Projection{}, err
		}
		visible, err := structurallyVisible(policy.Binding.Visibility, descriptor, policy.Grants)
		if err != nil {
			return Projection{}, ErrAuthorizationUnavailable
		}
		if visible {
			projected = append(projected, projectedTool{id: descriptor.ID, tool: projectTool(descriptor)})
		}
	}
	sort.Slice(projected, func(left, right int) bool {
		if projected[left].tool.Name != projected[right].tool.Name {
			return projected[left].tool.Name < projected[right].tool.Name
		}
		return projected[left].id < projected[right].id
	})
	tools := make([]*Tool, len(projected))
	for index := range projected {
		tools[index] = projected[index].tool
	}
	if err := ctx.Err(); err != nil {
		return Projection{}, err
	}
	if request.Lease != nil && !request.Lease.Current() {
		return Projection{}, authorization.ErrAuthenticationRequired
	}
	if !service.catalog.IsCurrentGeneration(catalogSnapshot.Generation) {
		if request.Continuation != nil {
			return Projection{}, ErrStaleCursor
		}
		return Projection{}, ErrAuthorizationUnavailable
	}
	return Projection{Tools: tools, Snapshot: resultSnapshot}, nil
}

func mapPolicyError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, authorization.ErrAuthenticationRequired) {
		return err
	}
	return ErrAuthorizationUnavailable
}

func snapshotFrom(policy authorization.DiscoveryPolicy, generation catalog.CurrentGeneration) Snapshot {
	return Snapshot{
		Generation: generation, PrincipalID: policy.Binding.PrincipalID,
		PrincipalRevision: policy.Binding.PrincipalRevision, Visibility: policy.Binding.Visibility,
		CredentialID: policy.Binding.CredentialID, CredentialRevision: policy.Binding.CredentialRevision,
		AuthorizationRevision: policy.AuthorizationRevision, EvaluatedAt: policy.EvaluatedAt.UTC(),
	}
}

func sameSnapshot(left, right Snapshot) bool {
	return left.Generation == right.Generation && left.PrincipalID == right.PrincipalID &&
		left.PrincipalRevision == right.PrincipalRevision && left.Visibility == right.Visibility &&
		left.CredentialID == right.CredentialID && left.CredentialRevision == right.CredentialRevision &&
		left.AuthorizationRevision == right.AuthorizationRevision && left.EvaluatedAt.Equal(right.EvaluatedAt)
}

func structurallyVisible(visibility contract.PrincipalVisibility, descriptor contract.ToolDescriptor, grants []authorization.StructuralGrant) (bool, error) {
	if visibility == contract.VisibilityAll {
		return true, nil
	}
	allowed := false
	unconstrainedDeny := false
	for _, grant := range grants {
		if grant.ServerID != descriptor.ServerID || grant.UpstreamName != nil && *grant.UpstreamName != descriptor.UpstreamName {
			continue
		}
		switch grant.Effect {
		case contract.GrantAllow:
			allowed = true
		case contract.GrantDeny:
			unconstrainedDeny = unconstrainedDeny || !grant.Constrained
		default:
			return false, ErrAuthorizationUnavailable
		}
	}
	switch visibility {
	case contract.VisibilityRequestable:
		return !unconstrainedDeny, nil
	case contract.VisibilityAllowedOnly:
		return allowed && !unconstrainedDeny, nil
	default:
		return false, ErrAuthorizationUnavailable
	}
}

func projectTool(descriptor contract.ToolDescriptor) *Tool {
	normalized := descriptor.Descriptor
	annotations := &ToolAnnotations{
		ReadOnlyHint: normalized.Annotations.ReadOnlyHint, DestructiveHint: boolPointer(normalized.Annotations.DestructiveHint),
		IdempotentHint: normalized.Annotations.IdempotentHint, OpenWorldHint: boolPointer(normalized.Annotations.OpenWorldHint),
	}
	if normalized.Annotations.Title != nil {
		annotations.Title = *normalized.Annotations.Title
	}
	tool := &Tool{
		Name: descriptor.ExternalName, InputSchema: append(json.RawMessage(nil), normalized.InputSchema...), Annotations: annotations,
	}
	if normalized.Title != nil {
		tool.Title = *normalized.Title
	}
	if normalized.Description != nil {
		tool.Description = *normalized.Description
	}
	if len(normalized.OutputSchema) > 0 {
		tool.OutputSchema = append(json.RawMessage(nil), normalized.OutputSchema...)
	}
	return tool
}

func boolPointer(value bool) *bool {
	copy := value
	return &copy
}
