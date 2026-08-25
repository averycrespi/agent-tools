package authorization

import "github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"

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

type PrincipalPage struct {
	Items []contract.Principal
	Next  *SnapshotCursor
}

type GrantPage struct {
	Items []contract.Grant
	Next  *SnapshotCursor
}
