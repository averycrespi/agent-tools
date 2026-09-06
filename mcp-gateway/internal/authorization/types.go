package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

const (
	principalCollection = "principals"
	grantCollection     = "grants"
)

type SnapshotCursor struct {
	Collection  string `json:"c"`
	PrincipalID string `json:"p,omitempty"`
	ServerID    string `json:"s,omitempty"`
	Upper       int64  `json:"u"`
	After       int64  `json:"a"`
	AfterID     string `json:"i"`
	Expires     int64  `json:"e"`
	Seal        string `json:"h"`
	Query       string `json:"q,omitempty"`
}

type GrantFilter struct {
	PrincipalID string
	ServerID    string
}

type CreatePrincipalRequest struct {
	DisplayName string
	Visibility  contract.PrincipalVisibility
}

type PatchPrincipalRequest struct {
	ExpectedRevision string
	DisplayName      *string
	State            *contract.PrincipalState
	Visibility       *contract.PrincipalVisibility
}

type CreateGrantRequest struct {
	Description  *string
	PrincipalID  string
	Effect       contract.GrantEffect
	ServerID     string
	UpstreamName *string
	Constraint   *json.RawMessage
	ExpiresAt    *time.Time
}

type PatchGrantRequest struct {
	ExpectedRevision string
	Description      **string
}

type CurrentGrantTargetValidator func(context.Context, *sql.Tx, string) (bool, error)

type EvaluationRequest struct {
	PrincipalID  string
	ServerID     string
	UpstreamName string
	Arguments    json.RawMessage
}

type ResolvedVerification struct {
	ServerID                      string
	UpstreamName                  string
	Arguments                     strictjson.Value
	ObservedAuthorizationRevision string
}

type BindingVerification struct {
	AuthorizationRevision string
	EvaluatedAt           string
}

type ResolvedVerificationPhase string

const (
	ResolvedUnverified      ResolvedVerificationPhase = "unverified"
	ResolvedBindingVerified ResolvedVerificationPhase = "binding_verified"
	ResolvedEvaluated       ResolvedVerificationPhase = "evaluated"
)

type PrincipalPage struct {
	contract.CollectionRange
	Items []contract.Principal
	Next  *SnapshotCursor
}

type GrantPage struct {
	Items []contract.Grant
	Next  *SnapshotCursor
}
