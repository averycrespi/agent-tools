package invocation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var (
	ErrInvalidInput        = errors.New("invocation input is invalid")
	ErrIdentityUnavailable = errors.New("invocation identity is unavailable")
	ErrInvalidState        = errors.New("invocation durable state is invalid")
	ErrStorageUnavailable  = errors.New("invocation storage is unavailable")
	ErrNotFound            = errors.New("invocation is not found")
	ErrInvalidCursor       = errors.New("invocation cursor is invalid")
	ErrStaleCursor         = errors.New("invocation cursor is stale")
)

type Clock interface{ Now() time.Time }
type Admission struct {
	activity.Admission
	MCP MCPDetails
}
type PreparedAdmission struct {
	activity.Identity
	admission Admission
}

type Repository struct {
	traffic *TrafficStore
	// store is used only by historical single-store readers and migration fixtures.
	store      *storage.Store
	clock      Clock
	entropy    io.Reader
	invalidate func(contract.Invalidation)
	limit      int64
	entropyMu  sync.Mutex
	cursorKey  [32]byte
}

func NewTrafficRepository(traffic *TrafficStore, clock Clock, entropy io.Reader, invalidate func(contract.Invalidation)) (*Repository, error) {
	if traffic == nil || clock == nil || entropy == nil || invalidate == nil {
		return nil, ErrInvalidInput
	}
	r := &Repository{traffic: traffic, clock: clock, entropy: entropy, invalidate: invalidate, limit: traffic.config.RetainedRecords}
	if _, err := rand.Read(r.cursorKey[:]); err != nil {
		return nil, err
	}
	traffic.mu.Lock()
	traffic.invalidate = invalidate
	traffic.mu.Unlock()
	return r, nil
}
func (r *Repository) Prepare(admission Admission) (PreparedAdmission, error) {
	now := r.clock.Now()
	at, ok := canonicalInvocationTimestamp(now)
	if !ok {
		return PreparedAdmission{}, ErrIdentityUnavailable
	}
	admission = cloneAdmissionEvidence(admission)
	if !validAdmission(admission, at) {
		return PreparedAdmission{}, ErrInvalidInput
	}
	p, err := r.prepareIdentityAt(now, at)
	if err != nil {
		return PreparedAdmission{}, err
	}
	p.admission = admission
	return p, nil
}
func (r *Repository) PrepareIdentity() (PreparedAdmission, error) {
	now := r.clock.Now()
	at, ok := canonicalInvocationTimestamp(now)
	if !ok {
		return PreparedAdmission{}, ErrIdentityUnavailable
	}
	return r.prepareIdentityAt(now, at)
}
func (r *Repository) prepareIdentityAt(now time.Time, at string) (PreparedAdmission, error) {
	r.entropyMu.Lock()
	id, err := admin.NewID(now, r.entropy)
	r.entropyMu.Unlock()
	if err != nil {
		return PreparedAdmission{}, fmt.Errorf("%w: generate invocation ID", ErrIdentityUnavailable)
	}
	return PreparedAdmission{Identity: activity.Identity{InvocationID: id, AdmittedAt: at}}, nil
}
func (p PreparedAdmission) WithAdmission(admission Admission) (PreparedAdmission, error) {
	admission = cloneAdmissionEvidence(admission)
	if p.admission.Class != "" || !validOpaqueInvocationID(p.InvocationID) || !validAdmission(admission, p.AdmittedAt) {
		return PreparedAdmission{}, ErrInvalidInput
	}
	p.admission = admission
	return p, nil
}
func encodeFailureDiagnostics(terminal contract.InvocationTerminalClass, diagnostic *contract.FailureDiagnostics) (any, error) {
	if !diagnostic.ValidFor(terminal) {
		return nil, ErrInvalidInput
	}
	if diagnostic == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(diagnostic)
	if err != nil || len(encoded) > contract.FailureDiagnosticMaxBytes {
		return nil, ErrInvalidInput
	}
	return string(encoded), nil
}
func (r *Repository) publish(invocationID string) {
	if r.invalidate == nil {
		return
	}
	id := invocationID
	r.invalidate(contract.Invalidation{Kind: contract.InvalidationInvocations, ResourceID: &id})
}
func (r *Repository) Read(ctx context.Context, id string) (contract.InvocationAuditRecord, bool, error) {
	if !validOpaqueInvocationID(id) {
		return contract.InvocationAuditRecord{}, false, ErrInvalidInput
	}
	var record contract.InvocationAuditRecord
	err := r.view(ctx, func(tx *sql.Tx) error {
		var err error
		record, err = scanInvocation(tx.QueryRowContext(ctx, invocationSelect+` WHERE id=?`, id))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return contract.InvocationAuditRecord{}, false, nil
	}
	return record, err == nil, err
}
func (r *Repository) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.view(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count)
	})
	return count, err
}
func (r *Repository) view(ctx context.Context, read func(*sql.Tx) error) error {
	if r.traffic != nil {
		return r.traffic.view(ctx, read)
	}
	if r.store == nil || r.store.Latched() {
		return ErrStorageUnavailable
	}
	err := r.store.View(ctx, func(tx *sql.Tx) error {
		if r.store.Latched() {
			return ErrStorageUnavailable
		}
		return read(tx)
	})
	if r.store.Latched() {
		return ErrStorageUnavailable
	}
	if err == nil || isInvocationError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
}
func isInvocationError(err error) bool {
	return errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrIdentityUnavailable) || errors.Is(err, ErrInvalidState) || errors.Is(err, ErrStorageUnavailable) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidCursor) || errors.Is(err, ErrStaleCursor)
}
func admissionSQLValues(p PreparedAdmission) ([]any, error) {
	credentialRevision, _ := strconv.ParseInt(p.admission.CredentialRevision, 10, 64)
	values := []any{p.InvocationID, p.admission.PrincipalID, p.admission.CredentialID, p.admission.CredentialFingerprint, credentialRevision, p.AdmittedAt, string(p.admission.Class), nullableString(p.admission.MCP.RequestedName), nullableBytes(p.admission.MCP.RedactedArguments)}
	if p.admission.MCP.Route == nil {
		values = append(values, nil, nil, nil, nil, nil)
	} else {
		route := p.admission.MCP.Route
		revision, _ := strconv.ParseInt(route.DescriptorRevision, 10, 64)
		values = append(values, route.Target.ServerID, route.ToolID, route.Target.ToolName(), revision, route.DescriptorFingerprint)
	}
	if p.admission.Authorization == nil {
		values = append(values, nil, nil, nil, nil)
	} else {
		revision, _ := strconv.ParseInt(p.admission.Authorization.AuthorizationRevision, 10, 64)
		values = append(values, string(p.admission.Authorization.Decision), revision, p.admission.Authorization.EvaluatedAt, nullableString(p.admission.Authorization.GrantID))
	}
	return values, nil
}
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableBytes(value []byte) any {
	if value == nil {
		return nil
	}
	return string(value)
}
func cloneAdmissionEvidence(value Admission) Admission {
	clone := value
	clone.MCP = cloneMCPDetails(value.MCP)
	if value.Authorization != nil {
		authorization := *value.Authorization
		if value.Authorization.GrantID != nil {
			grantID := *value.Authorization.GrantID
			authorization.GrantID = &grantID
		}
		clone.Authorization = &authorization
	}
	return clone
}
