package grantrequests

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

var (
	ErrInvalidInput        = errors.New("grant request input is invalid")
	ErrIdentityUnavailable = errors.New("grant request identity is unavailable")
	ErrInvalidState        = errors.New("grant request durable state is invalid")
	ErrStaleCursor         = errors.New("grant request cursor is stale")
	ErrStorageUnavailable  = errors.New("grant request storage is unavailable")
)

type Clock interface {
	Now() time.Time
}

type NamespaceInspector interface {
	LookupNamespaceTargetTx(context.Context, *sql.Tx, string) (servers.NamespaceTarget, error)
}

type DescriptorInspector interface {
	LookupDurableDescriptorTx(context.Context, *sql.Tx, string, string) (catalog.DurableDescriptor, error)
}

type DenyInspector interface {
	HasActiveDenyConflictTx(context.Context, *sql.Tx, authorization.DenyConflictScope, time.Time) (bool, error)
}

type Options struct {
	Store       *storage.Store
	Clock       Clock
	Entropy     io.Reader
	Namespaces  NamespaceInspector
	Descriptors DescriptorInspector
	Denies      DenyInspector
	Invalidate  func(contract.Invalidation)
}

type Repository struct {
	store       *storage.Store
	clock       Clock
	entropy     io.Reader
	namespaces  NamespaceInspector
	descriptors DescriptorInspector
	denies      DenyInspector
	invalidate  func(contract.Invalidation)
	entropyMu   sync.Mutex
}

type CreateRequest struct {
	PrincipalID string
	Policy      contract.Policy
}

func New(options Options) (*Repository, error) {
	if options.Store == nil || options.Clock == nil || options.Entropy == nil || options.Namespaces == nil ||
		options.Descriptors == nil || options.Denies == nil || options.Invalidate == nil {
		return nil, errors.New("grant request repository dependencies are incomplete")
	}
	return &Repository{
		store: options.Store, clock: options.Clock, entropy: options.Entropy,
		namespaces: options.Namespaces, descriptors: options.Descriptors, denies: options.Denies,
		invalidate: options.Invalidate,
	}, nil
}
