package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
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

type Clock interface {
	Now() time.Time
}

type RouteEvidence struct {
	ServerID              string
	ToolID                string
	UpstreamName          string
	DescriptorRevision    string
	DescriptorFingerprint string
}

type AuthorizationEvidence struct {
	Decision              contract.AuthorizationDecision
	AuthorizationRevision string
	EvaluatedAt           string
	GrantID               *string
}

type Admission struct {
	PrincipalID           string
	CredentialID          string
	CredentialFingerprint string
	CredentialRevision    string
	Class                 contract.InvocationAdmissionClass
	RequestedName         *string
	RedactedArguments     []byte
	Route                 *RouteEvidence
	Authorization         *AuthorizationEvidence
}

type PreparedAdmission struct {
	InvocationID string
	AdmittedAt   string
	admission    Admission
}

type Repository struct {
	store     *storage.Store
	clock     Clock
	entropy   io.Reader
	entropyMu sync.Mutex
}

func NewRepository(store *storage.Store, clock Clock, entropy io.Reader) (*Repository, error) {
	if store == nil || clock == nil || entropy == nil {
		return nil, errors.New("invocation repository dependencies are incomplete")
	}
	return &Repository{store: store, clock: clock, entropy: entropy}, nil
}

func (repository *Repository) Prepare(admission Admission) (PreparedAdmission, error) {
	now := repository.clock.Now()
	admittedAt, ok := canonicalInvocationTimestamp(now)
	if !ok {
		return PreparedAdmission{}, ErrIdentityUnavailable
	}
	admission = cloneAdmissionEvidence(admission)
	if !validAdmission(admission, admittedAt) {
		return PreparedAdmission{}, ErrInvalidInput
	}
	identity, err := repository.prepareIdentityAt(now, admittedAt)
	if err != nil {
		return PreparedAdmission{}, err
	}
	identity.admission = admission
	return identity, nil
}

func (repository *Repository) PrepareIdentity() (PreparedAdmission, error) {
	now := repository.clock.Now()
	admittedAt, ok := canonicalInvocationTimestamp(now)
	if !ok {
		return PreparedAdmission{}, ErrIdentityUnavailable
	}
	return repository.prepareIdentityAt(now, admittedAt)
}

func (repository *Repository) prepareIdentityAt(now time.Time, admittedAt string) (PreparedAdmission, error) {
	repository.entropyMu.Lock()
	id, err := admin.NewID(now, repository.entropy)
	repository.entropyMu.Unlock()
	if err != nil {
		return PreparedAdmission{}, fmt.Errorf("%w: generate invocation ID", ErrIdentityUnavailable)
	}
	return PreparedAdmission{InvocationID: id, AdmittedAt: admittedAt}, nil
}

func (prepared PreparedAdmission) WithAdmission(admission Admission) (PreparedAdmission, error) {
	admission = cloneAdmissionEvidence(admission)
	if prepared.admission.Class != "" || !validOpaqueInvocationID(prepared.InvocationID) || !validAdmission(admission, prepared.AdmittedAt) {
		return PreparedAdmission{}, ErrInvalidInput
	}
	prepared.admission = admission
	return prepared, nil
}

func (repository *Repository) Insert(ctx context.Context, prepared PreparedAdmission) error {
	return repository.mutate(ctx, func(transaction *sql.Tx) error {
		return repository.InsertTx(ctx, transaction, prepared)
	})
}

func (repository *Repository) InsertTx(ctx context.Context, transaction *sql.Tx, prepared PreparedAdmission) error {
	if transaction == nil || !validPreparedAdmission(prepared) {
		return ErrInvalidInput
	}
	var exists int
	if err := transaction.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM invocations WHERE id = ?)`, prepared.InvocationID).Scan(&exists); err != nil {
		return fmt.Errorf("inspect invocation identity: %w", err)
	}
	if exists != 0 {
		return ErrIdentityUnavailable
	}
	var count int64
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count); err != nil {
		return fmt.Errorf("count invocations before insert: %w", err)
	}
	if excess := count - invocationLimit() + 1; excess > 0 {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM invocations WHERE insertion_sequence IN (
			SELECT insertion_sequence FROM invocations ORDER BY insertion_sequence LIMIT ?
		)`, excess); err != nil {
			return fmt.Errorf("evict oldest invocations: %w", err)
		}
	}
	values, err := admissionSQLValues(prepared)
	if err != nil {
		return err
	}
	_, err = transaction.ExecContext(ctx, `INSERT INTO invocations (
		id, principal_id, credential_id, credential_fingerprint, credential_revision,
		admitted_at, admission_class, requested_name, redacted_arguments,
		server_id, tool_id, upstream_name, descriptor_revision, descriptor_fingerprint,
		decision, authorization_revision, evaluated_at, grant_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, values...)
	if err != nil {
		return fmt.Errorf("insert invocation admission: %w", err)
	}
	return nil
}

func (repository *Repository) AnnotateTerminal(ctx context.Context, invocationID string, terminal contract.InvocationTerminalClass) error {
	if !validOpaqueInvocationID(invocationID) {
		return ErrInvalidInput
	}
	if _, err := contract.ParseInvocationTerminalClass(string(terminal)); err != nil {
		return ErrInvalidInput
	}
	completedAt, ok := canonicalInvocationTimestamp(repository.clock.Now())
	if !ok {
		return ErrInvalidInput
	}
	return repository.mutate(ctx, func(transaction *sql.Tx) error {
		var admittedAt, evaluatedAt string
		err := transaction.QueryRowContext(ctx, `SELECT admitted_at, evaluated_at FROM invocations
			WHERE id = ? AND admission_class = 'evaluated' AND decision = 'allow'
			  AND completed_at IS NULL AND terminal_class IS NULL`, invocationID).Scan(&admittedAt, &evaluatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read invocation before terminal annotation: %w", err)
		}
		admitted, valid := parseCanonicalInvocationTimestamp(admittedAt)
		evaluated, evaluationValid := parseCanonicalInvocationTimestamp(evaluatedAt)
		completed, completeValid := parseCanonicalInvocationTimestamp(completedAt)
		if !valid || !evaluationValid || !completeValid || completed.Before(admitted) || completed.Before(evaluated) {
			return ErrInvalidInput
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE invocations
			SET completed_at = ?, terminal_class = ?
			WHERE id = ? AND completed_at IS NULL AND terminal_class IS NULL`, completedAt, string(terminal), invocationID); err != nil {
			return fmt.Errorf("annotate invocation terminal result: %w", err)
		}
		return nil
	})
}

func (repository *Repository) Read(ctx context.Context, invocationID string) (contract.InvocationAuditRecord, bool, error) {
	if !validOpaqueInvocationID(invocationID) {
		return contract.InvocationAuditRecord{}, false, ErrInvalidInput
	}
	var record contract.InvocationAuditRecord
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		row := transaction.QueryRowContext(ctx, invocationSelect+` WHERE id = ?`, invocationID)
		var scanErr error
		record, scanErr = scanInvocation(row)
		return scanErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return contract.InvocationAuditRecord{}, false, nil
	}
	return record, err == nil, err
}

func (repository *Repository) Count(ctx context.Context) (int64, error) {
	var count int64
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count)
	})
	return count, err
}

func (repository *Repository) mutate(ctx context.Context, callback func(*sql.Tx) error) error {
	err := repository.store.Mutate(ctx, callback)
	if err == nil || isInvocationError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
}

func (repository *Repository) view(ctx context.Context, callback func(*sql.Tx) error) error {
	if repository.store.Latched() {
		return ErrStorageUnavailable
	}
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		if repository.store.Latched() {
			return ErrStorageUnavailable
		}
		return callback(transaction)
	})
	if repository.store.Latched() {
		return ErrStorageUnavailable
	}
	if err == nil || isInvocationError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
}

func isInvocationError(err error) bool {
	return errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrIdentityUnavailable) || errors.Is(err, ErrInvalidState) ||
		errors.Is(err, ErrStorageUnavailable) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidCursor) || errors.Is(err, ErrStaleCursor)
}

func admissionSQLValues(prepared PreparedAdmission) ([]any, error) {
	credentialRevision, _ := strconv.ParseInt(prepared.admission.CredentialRevision, 10, 64)
	values := []any{
		prepared.InvocationID, prepared.admission.PrincipalID, prepared.admission.CredentialID,
		prepared.admission.CredentialFingerprint, credentialRevision, prepared.AdmittedAt, string(prepared.admission.Class),
		nullableString(prepared.admission.RequestedName), nullableBytes(prepared.admission.RedactedArguments),
	}
	if prepared.admission.Route == nil {
		values = append(values, nil, nil, nil, nil, nil)
	} else {
		descriptorRevision, _ := strconv.ParseInt(prepared.admission.Route.DescriptorRevision, 10, 64)
		values = append(values, prepared.admission.Route.ServerID, prepared.admission.Route.ToolID, prepared.admission.Route.UpstreamName,
			descriptorRevision, prepared.admission.Route.DescriptorFingerprint)
	}
	if prepared.admission.Authorization == nil {
		values = append(values, nil, nil, nil, nil)
	} else {
		authorizationRevision, _ := strconv.ParseInt(prepared.admission.Authorization.AuthorizationRevision, 10, 64)
		values = append(values, string(prepared.admission.Authorization.Decision), authorizationRevision,
			prepared.admission.Authorization.EvaluatedAt, nullableString(prepared.admission.Authorization.GrantID))
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
	clone.RedactedArguments = append([]byte(nil), value.RedactedArguments...)
	if value.RequestedName != nil {
		name := *value.RequestedName
		clone.RequestedName = &name
	}
	if value.Route != nil {
		route := *value.Route
		clone.Route = &route
	}
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
