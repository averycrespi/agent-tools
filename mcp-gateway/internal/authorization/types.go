package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const (
	principalCollection = "principals"
	grantCollection     = "grants"
)

type SnapshotCursor struct {
	Collection  string
	PrincipalID string
	ServerID    string
	Upper       int64
	After       int64
	AfterID     string
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
	PrincipalID  string
	Effect       contract.GrantEffect
	ServerID     string
	UpstreamName *string
	Constraint   *json.RawMessage
	ExpiresAt    *time.Time
}

type CurrentGrantTargetValidator func(context.Context, *sql.Tx, string) (bool, error)

type PrincipalPage struct {
	Items []contract.Principal
	Next  *SnapshotCursor
}

type GrantPage struct {
	Items []contract.Grant
	Next  *SnapshotCursor
}
