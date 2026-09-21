package authorization

import (
	"context"
	"database/sql"
	"sync/atomic"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// EvaluationCandidate is process-local and sealed to the exact pending lease,
// invocation identity and single evaluation. It carries no ongoing gate scope.
// It is not dispatch authority until ConfirmEvaluation returns its subject.
type EvaluationCandidate struct {
	repository   *Repository
	lease        *Lease
	invocationID string
	revision     string
	result       contract.AuthorizationResult
	used         atomic.Bool
}

type Evaluation struct {
	Result    contract.AuthorizationResult
	Phase     ResolvedVerificationPhase
	Candidate *EvaluationCandidate
}

func (repository *Repository) EvaluateAdmission(ctx context.Context, lease *Lease, invocationID string, request *ResolvedVerification) (evaluation Evaluation, err error) {
	if !validOpaqueID(invocationID) {
		return evaluation, ErrInvalidInput
	}
	err = repository.WithAdmission(ctx, lease, func(admission *Admission) error {
		return repository.view(ctx, func(tx *sql.Tx) error {
			if request == nil {
				_, e := admission.VerifyBindingOnlyTx(ctx, tx)
				return e
			}
			var e error
			evaluation.Result, _, evaluation.Phase, e = admission.VerifyResolvedTx(ctx, tx, *request)
			if e == nil && evaluation.Result.Decision == contract.DecisionAllow {
				sealed := evaluation.Result
				if sealed.GrantID != nil {
					grant := *sealed.GrantID
					sealed.GrantID = &grant
				}
				evaluation.Candidate = &EvaluationCandidate{repository: repository, lease: lease, invocationID: invocationID, revision: evaluation.Result.AuthorizationRevision, result: sealed}
			}
			return e
		})
	})
	return evaluation, err
}

// ConfirmEvaluation reacquires authority only after persistence settlement. The
// receipt owner invokes detach under its short fault fence, never its SQL writer.
// All failures consume the candidate: neither reevaluation nor retry is allowed.
func (repository *Repository) ConfirmEvaluation(ctx context.Context, candidate *EvaluationCandidate, invocationID string, confirmReceipt func(contract.AuthorizationResult, func() bool) bool) (AdmittedSubject, error) {
	if candidate == nil || candidate.repository != repository || candidate.invocationID != invocationID || confirmReceipt == nil || !candidate.used.CompareAndSwap(false, true) {
		return AdmittedSubject{}, ErrAdmissionUnavailable
	}
	lease := candidate.lease
	release, err := repository.authority.acquire(ctx)
	if err != nil {
		return AdmittedSubject{}, err
	}
	defer release()
	err = repository.view(ctx, func(tx *sql.Tx) error {
		if err := verifyCurrentBindingTx(ctx, tx, lease.binding); err != nil {
			return err
		}
		revision, err := authorizationRevisionTx(ctx, tx)
		if err != nil {
			return err
		}
		if revision != candidate.revision {
			return ErrAuthorizationUnavailable
		}
		return nil
	})
	if err != nil {
		return AdmittedSubject{}, err
	}
	registry := repository.authority
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.draining.Load() || ctx.Err() != nil {
		return AdmittedSubject{}, ErrAdmissionUnavailable
	}
	confirmed := repository.store.ConfirmHealthy(func() bool {
		return confirmReceipt(candidate.result, func() bool {
			if ctx.Err() != nil || !lease.phase.CompareAndSwap(uint32(leasePending), uint32(leaseAdmitted)) {
				return false
			}
			delete(registry.leases, lease)
			return true
		})
	})
	if !confirmed {
		return AdmittedSubject{}, ErrAdmissionUnavailable
	}
	return admittedSubject(repository, lease.binding, candidate.revision), nil
}
