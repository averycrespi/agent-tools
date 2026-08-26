package authorization

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type Admission struct {
	repository *Repository
	lease      *Lease
	lifecycle  sync.RWMutex
	active     bool
	verified   atomic.Bool
}

type PendingDetachment struct {
	admission *Admission
	lease     *Lease
	committed atomic.Bool
}

func (repository *Repository) WithAdmission(ctx context.Context, lease *Lease, use func(*Admission) error) error {
	if lease == nil || lease.owner != repository.authority || use == nil {
		return ErrInvalidInput
	}
	releaseGate, err := repository.authority.tryAcquire(ctx)
	if err != nil {
		return err
	}
	admission := &Admission{repository: repository, lease: lease, active: true}
	defer func() {
		admission.lifecycle.Lock()
		admission.active = false
		admission.lifecycle.Unlock()
		releaseGate()
	}()
	if leasePhase(lease.phase.Load()) != leasePending {
		return ErrAuthenticationRequired
	}
	return use(admission)
}

func (admission *Admission) VerifyResolvedTx(
	ctx context.Context,
	transaction *sql.Tx,
	request ResolvedVerification,
) (contract.AuthorizationResult, *PendingDetachment, ResolvedVerificationPhase, error) {
	if !validOpaqueID(request.ServerID) || !validUpstreamName(request.UpstreamName) ||
		request.Arguments.Type != strictjson.ValueObject ||
		!validObservedAuthorizationRevision(request.ObservedAuthorizationRevision) {
		return contract.AuthorizationResult{}, nil, ResolvedUnverified, ErrInvalidInput
	}
	finish, err := admission.beginVerification(ctx, transaction)
	if err != nil {
		return contract.AuthorizationResult{}, nil, ResolvedUnverified, err
	}
	defer finish()
	if err := verifyCurrentBindingTx(ctx, transaction, admission.lease.binding); err != nil {
		return contract.AuthorizationResult{}, nil, ResolvedUnverified, err
	}
	evaluatedAt := admission.repository.clock.Now().UTC()
	result, err := evaluateTx(
		ctx,
		transaction,
		admission.lease.binding.PrincipalID,
		request.ServerID,
		request.UpstreamName,
		request.Arguments,
		evaluatedAt,
	)
	if err != nil {
		return contract.AuthorizationResult{}, nil, ResolvedBindingVerified, err
	}
	if result.Decision != contract.DecisionAllow {
		return result, nil, ResolvedEvaluated, nil
	}
	return result, &PendingDetachment{admission: admission, lease: admission.lease}, ResolvedEvaluated, nil
}

func (admission *Admission) VerifyBindingOnlyTx(
	ctx context.Context,
	transaction *sql.Tx,
) (BindingVerification, error) {
	finish, err := admission.beginVerification(ctx, transaction)
	if err != nil {
		return BindingVerification{}, err
	}
	defer finish()
	if err := verifyCurrentBindingTx(ctx, transaction, admission.lease.binding); err != nil {
		return BindingVerification{}, err
	}
	revision, err := authorizationRevisionTx(ctx, transaction)
	if err != nil {
		return BindingVerification{}, err
	}
	return BindingVerification{
		AuthorizationRevision: revision,
		EvaluatedAt:           formatAuthorizationTime(admission.repository.clock.Now().UTC()),
	}, nil
}

func (admission *Admission) beginVerification(ctx context.Context, transaction *sql.Tx) (func(), error) {
	if admission == nil || admission.repository == nil || admission.lease == nil {
		return nil, ErrAdmissionUnavailable
	}
	admission.lifecycle.RLock()
	fail := func(err error) (func(), error) {
		admission.lifecycle.RUnlock()
		return nil, err
	}
	if !admission.active {
		return fail(ErrAdmissionUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if admission.repository.store.Latched() {
		return fail(ErrStorageUnavailable)
	}
	if transaction == nil {
		return fail(ErrInvalidInput)
	}
	if !admission.verified.CompareAndSwap(false, true) {
		return fail(ErrAdmissionUnavailable)
	}
	if leasePhase(admission.lease.phase.Load()) != leasePending {
		return fail(ErrAuthenticationRequired)
	}
	return admission.lifecycle.RUnlock, nil
}

func (pending *PendingDetachment) CommitSucceeded() error {
	if pending == nil || pending.admission == nil || pending.lease == nil {
		return ErrAdmissionUnavailable
	}
	pending.admission.lifecycle.RLock()
	defer pending.admission.lifecycle.RUnlock()
	if !pending.admission.active {
		return ErrAdmissionUnavailable
	}
	if pending.committed.Load() {
		return nil
	}
	if !pending.lease.phase.CompareAndSwap(uint32(leasePending), uint32(leaseAdmitted)) {
		return ErrAdmissionUnavailable
	}
	pending.lease.owner.remove(pending.lease)
	pending.committed.Store(true)
	return nil
}

func verifyCurrentBindingTx(ctx context.Context, transaction *sql.Tx, binding CredentialBinding) error {
	var (
		principalID, state, visibility          string
		principalRevision, credentialRevision   int64
		credentialID, fingerprint, credentialAt sql.NullString
		verifier                                []byte
		createdAt, updatedAt                    string
	)
	err := transaction.QueryRowContext(ctx, `
		SELECT id, state, visibility, revision, credential_revision,
		       credential_id, credential_verifier, credential_fingerprint,
		       credential_created_at, created_at, updated_at
		FROM principals
		WHERE id = ?`, binding.PrincipalID).Scan(
		&principalID, &state, &visibility, &principalRevision, &credentialRevision,
		&credentialID, &verifier, &fingerprint, &credentialAt, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthenticationRequired
	}
	if err != nil {
		return fmt.Errorf("%w: verify current credential binding: %w", ErrStorageUnavailable, err)
	}
	created, createdValid := canonicalTimestamp(createdAt)
	updated, updatedValid := canonicalTimestamp(updatedAt)
	credentialPresent := credentialID.Valid || verifier != nil || fingerprint.Valid || credentialAt.Valid
	credentialComplete := credentialID.Valid && len(verifier) == 32 && fingerprint.Valid && credentialAt.Valid
	if !validOpaqueID(principalID) || !validPrincipalState(contract.PrincipalState(state)) ||
		!validVisibility(contract.PrincipalVisibility(visibility)) || principalRevision < 1 || credentialRevision < 0 ||
		!createdValid || !updatedValid || updated.Before(created) || credentialPresent != credentialComplete ||
		credentialComplete && credentialRevision == 0 {
		return ErrAuthorizationUnavailable
	}
	if state != string(contract.PrincipalActive) || !credentialComplete {
		if credentialComplete {
			return ErrAuthorizationUnavailable
		}
		return ErrAuthenticationRequired
	}
	issued, issuedValid := canonicalTimestamp(credentialAt.String)
	if !validOpaqueID(credentialID.String) || !validFingerprint(fingerprint.String) ||
		!issuedValid || issued.Before(created) || issued.After(updated) {
		return ErrAuthorizationUnavailable
	}
	current := CredentialBinding{
		PrincipalID: principalID, PrincipalRevision: strconv.FormatInt(principalRevision, 10),
		Visibility: contract.PrincipalVisibility(visibility), CredentialID: credentialID.String,
		CredentialRevision: strconv.FormatInt(credentialRevision, 10), CredentialFingerprint: fingerprint.String,
	}
	if current != binding {
		return ErrAuthenticationRequired
	}
	return nil
}

func authorizationRevisionTx(ctx context.Context, transaction *sql.Tx) (string, error) {
	var revision int64
	if err := transaction.QueryRowContext(ctx, `SELECT revision FROM authorization_meta WHERE singleton = 1`).Scan(&revision); err != nil {
		return "", fmt.Errorf("%w: read authorization revision: %w", ErrStorageUnavailable, err)
	}
	if revision < 0 {
		return "", ErrAuthorizationUnavailable
	}
	return strconv.FormatInt(revision, 10), nil
}

func validObservedAuthorizationRevision(revision string) bool {
	if revision == "" {
		return true
	}
	parsed, err := strconv.ParseInt(revision, 10, 64)
	return err == nil && parsed >= 0 && strconv.FormatInt(parsed, 10) == revision
}
