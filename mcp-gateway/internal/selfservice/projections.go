// Package selfservice owns the fixed synthetic catalog, agent projections, protected cursors, and local handlers.
package selfservice

import (
	"context"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

// ProjectionReader is the nonadministrative authorization view consumed by self-service handlers.
type ProjectionReader interface {
	ReadSelfIdentity(context.Context, authorization.AdmittedSubject) (contract.SelfIdentity, error)
	ListSelfGrants(context.Context, authorization.AdmittedSubject, *authorization.SelfGrantCursor, int) (authorization.SelfGrantPage, error)
}

var _ ProjectionReader = (*authorization.SelfProjectionService)(nil)
