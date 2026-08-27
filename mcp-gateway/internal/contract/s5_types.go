package contract

import "encoding/json"

const (
	SummaryIdentityReturned                  = "Identity returned."
	SummaryGrantsReturned                    = "Grants returned."
	SummaryGrantRequestProcessed             = "Grant request processed."
	SummaryGrantRequestReturned              = "Grant request returned."
	SummaryGrantRequestsReturned             = "Grant requests returned."
	SummaryGrantRequestCancellationProcessed = "Grant request cancellation processed."
)

type Policy struct {
	Scope                   PolicyScope      `json:"scope"`
	Target                  string           `json:"target"`
	Constraint              *json.RawMessage `json:"constraint"`
	DurationSeconds         *string          `json:"duration_seconds"`
	FutureToolsAcknowledged bool             `json:"future_tools_acknowledged"`
}

type GrantPolicy struct {
	Scope      PolicyScope      `json:"scope"`
	Target     string           `json:"target"`
	Constraint *json.RawMessage `json:"constraint"`
}

type SelfIdentity struct {
	ID                 string              `json:"id"`
	DisplayName        string              `json:"display_name"`
	State              PrincipalState      `json:"state"`
	Visibility         PrincipalVisibility `json:"visibility"`
	PrincipalRevision  string              `json:"principal_revision"`
	CredentialRevision string              `json:"credential_revision"`
}

type AgentGrant struct {
	ID        string      `json:"id"`
	Effect    GrantEffect `json:"effect"`
	Policy    GrantPolicy `json:"policy"`
	ExpiresAt *string     `json:"expires_at"`
	State     GrantState  `json:"state"`
	CreatedAt string      `json:"created_at"`
}

type AgentGrantRequest struct {
	ID              string                       `json:"id"`
	State           GrantRequestState            `json:"state"`
	Revision        string                       `json:"revision"`
	RequestedPolicy Policy                       `json:"requested_policy"`
	ApprovedPolicy  *Policy                      `json:"approved_policy"`
	ApprovedGrantID *string                      `json:"approved_grant_id"`
	RejectionReason *GrantRequestRejectionReason `json:"rejection_reason"`
	CreatedAt       string                       `json:"created_at"`
	UpdatedAt       string                       `json:"updated_at"`
	ClosedAt        *string                      `json:"closed_at"`
}

type DescriptorEvidence struct {
	ServerID        string                   `json:"server_id"`
	ToolID          string                   `json:"tool_id"`
	Namespace       string                   `json:"namespace"`
	UpstreamName    string                   `json:"upstream_name"`
	ExternalName    string                   `json:"external_name"`
	CatalogRevision string                   `json:"catalog_revision"`
	Fingerprint     string                   `json:"fingerprint"`
	DurableState    DescriptorEvidenceState  `json:"durable_state"`
	Descriptor      NormalizedToolDescriptor `json:"descriptor"`
	CapturedAt      string                   `json:"captured_at"`
}

type TargetComparison struct {
	Scope           PolicyScope               `json:"scope"`
	TargetState     TargetState               `json:"target_state"`
	ActiveState     *TargetActiveState        `json:"active_state"`
	DurableState    *TargetDurableState       `json:"durable_state"`
	CatalogRevision *string                   `json:"catalog_revision"`
	Fingerprint     *string                   `json:"fingerprint"`
	Descriptor      *NormalizedToolDescriptor `json:"descriptor"`
}

type GrantRequestSummary struct {
	ID              string                       `json:"id"`
	PrincipalID     string                       `json:"principal_id"`
	State           GrantRequestState            `json:"state"`
	Revision        string                       `json:"revision"`
	RequestedPolicy Policy                       `json:"requested_policy"`
	ApprovedPolicy  *Policy                      `json:"approved_policy"`
	ApprovedGrantID *string                      `json:"approved_grant_id"`
	RejectionReason *GrantRequestRejectionReason `json:"rejection_reason"`
	CreatedAt       string                       `json:"created_at"`
	UpdatedAt       string                       `json:"updated_at"`
	ClosedAt        *string                      `json:"closed_at"`
}

type GrantRequest struct {
	GrantRequestSummary
	ResolvedServerID     string              `json:"resolved_server_id"`
	ResolvedUpstreamName *string             `json:"resolved_upstream_name"`
	SubmittedEvidence    *DescriptorEvidence `json:"submitted_evidence"`
	ApprovedEvidence     *DescriptorEvidence `json:"approved_evidence"`
	CurrentTarget        TargetComparison    `json:"current_target"`
}

type GrantRequestApproval struct {
	ApprovedPolicy Policy `json:"approved_policy"`
}

type GrantRequestRejection struct {
	Reason GrantRequestRejectionReason `json:"reason"`
}

type GetIdentityResult struct {
	Identity SelfIdentity `json:"identity"`
}

type ListGrantsInput struct {
	Cursor *string `json:"cursor"`
}

type ListGrantsResult struct {
	Outcome    CursorOutcome `json:"outcome"`
	Items      []AgentGrant  `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

type CreateGrantRequestInput struct {
	Policy Policy `json:"policy"`
}

type CreateGrantRequestResult struct {
	Outcome CreateGrantRequestOutcome `json:"outcome"`
	Request *AgentGrantRequest        `json:"request"`
}

type GrantRequestIDInput struct {
	ID string `json:"id"`
}

type GetGrantRequestResult struct {
	Outcome GetGrantRequestOutcome `json:"outcome"`
	Request *AgentGrantRequest     `json:"request"`
}

type ListGrantRequestsInput struct {
	Cursor *string            `json:"cursor"`
	State  *GrantRequestState `json:"state"`
}

type ListGrantRequestsResult struct {
	Outcome    CursorOutcome       `json:"outcome"`
	Items      []AgentGrantRequest `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

type CancelGrantRequestResult struct {
	Outcome CancelGrantRequestOutcome `json:"outcome"`
	Request *AgentGrantRequest        `json:"request"`
}
