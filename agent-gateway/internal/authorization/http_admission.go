package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

// HTTPEvaluationCandidate seals one coherent evaluation without retaining an
// authority lock, submitted URL or executable callback across persistence.
type HTTPEvaluationCandidate struct {
	repository             *Repository
	lease                  *Lease
	id, revision, evidence string
	material               *contract.HTTPTrafficMaterial
	used                   atomic.Bool
}

type HTTPEvaluation struct {
	Evidence  contract.HTTPTrafficAdmission
	Candidate *HTTPEvaluationCandidate
}

func (r *Repository) EvaluateHTTPAdmission(ctx context.Context, lease *Lease, id, admittedAt string, in HTTPAccessInput, facts httppolicy.AddressFacts) (out HTTPEvaluation, err error) {
	if !validOpaqueID(id) || lease == nil || in.PrincipalID != lease.binding.PrincipalID {
		return out, ErrInvalidInput
	}
	admitted, err := time.Parse(time.RFC3339Nano, admittedAt)
	if err != nil || formatAuthorizationTime(admitted) != admittedAt {
		return out, ErrInvalidInput
	}
	target, parseErr := parseHTTPTarget(in)
	err = r.WithAdmission(ctx, lease, func(admission *Admission) error {
		return r.view(ctx, func(tx *sql.Tx) error {
			binding, err := admission.VerifyBindingOnlyTx(ctx, tx)
			if err != nil {
				return err
			}
			now, err := time.Parse(time.RFC3339Nano, binding.EvaluatedAt)
			if err != nil || now.Before(admitted) {
				return ErrInvalidInput
			}
			principalRevision, err := httpRevision(lease.binding.PrincipalRevision)
			if err != nil {
				return err
			}
			credentialRevision, err := httpRevision(lease.binding.CredentialRevision)
			if err != nil {
				return err
			}
			evidence := contract.HTTPTrafficAdmission{ID: id, AdmittedAt: admittedAt, Principal: contract.HTTPRevisionRef{ID: lease.binding.PrincipalID, Revision: principalRevision}, AgentCredential: contract.HTTPRevisionRef{ID: lease.binding.CredentialID, Revision: credentialRevision}, CredentialFingerprint: lease.binding.CredentialFingerprint, Class: "invalid_request", EvaluatedAt: binding.EvaluatedAt, Grants: []contract.HTTPTrafficGrant{}}
			if parseErr != nil {
				out.Evidence = evidence
				return nil
			}
			evaluator, defaultPolicy, err := httpEvaluatorTx(ctx, tx, in.PrincipalID, now)
			if err != nil {
				return err
			}
			var decision contract.HTTPDecision
			if target.connect {
				decision, err = evaluator.Connect(target.destination, facts)
			} else {
				decision, err = evaluator.Request(target.request, facts)
			}
			if err != nil {
				return err
			}
			evidence.Class = "evaluated"
			evidence.Default = defaultPolicy
			evidence.Decision = &decision
			evidence.Target = &contract.HTTPTrafficTarget{Host: target.destination.Host(), Port: target.destination.Port()}
			if !target.connect {
				evidence.Target.Scheme = target.request.Scheme()
				evidence.Target.Method = target.request.Method()
			}
			seen := map[string]bool{}
			for _, ref := range []*contract.HTTPRevisionRef{decision.Grant, decision.PrivateGrant, decision.CredentialGrant, decision.ConflictGrant} {
				if ref == nil || seen[ref.ID] {
					continue
				}
				seen[ref.ID] = true
				grant, _, err := httpGrantTx(ctx, tx, ref.ID, now)
				if err != nil || grant.Revision != strconv.FormatUint(ref.Revision, 10) {
					return ErrInvalidState
				}
				var policy contract.HTTPPolicy
				if json.Unmarshal(grant.Policy, &policy) != nil {
					return ErrInvalidState
				}
				evidence.Grants = append(evidence.Grants, contract.HTTPTrafficGrant{Reference: *ref, Policy: policy})
			}
			slices.SortFunc(evidence.Grants, func(a, b contract.HTTPTrafficGrant) int {
				if a.Reference.ID < b.Reference.ID {
					return -1
				}
				if a.Reference.ID > b.Reference.ID {
					return 1
				}
				return 0
			})
			if decision.Allowed && decision.Credential != nil {
				generation, err := httpcredentials.MaterialGenerationTx(ctx, tx, *decision.Credential)
				if err != nil {
					return err
				}
				evidence.Material = &contract.HTTPTrafficMaterial{Credential: *decision.Credential, Generation: generation}
			}
			out.Evidence = evidence
			if decision.Allowed {
				encoded, err := json.Marshal(evidence)
				if err != nil || len(encoded) > contract.HTTPTrafficAdmissionBytes {
					return ErrAuthorizationUnavailable
				}
				var material *contract.HTTPTrafficMaterial
				if evidence.Material != nil {
					copy := *evidence.Material
					material = &copy
				}
				out.Candidate = &HTTPEvaluationCandidate{repository: r, lease: lease, id: id, revision: binding.AuthorizationRevision, evidence: string(encoded), material: material}
			}
			return nil
		})
	})
	if err != nil {
		return HTTPEvaluation{}, err
	}
	return out, nil
}

// ConfirmHTTP consumes the sealed candidate even on refusal. The evidence owner
// performs only an immutable comparison and single receipt disposition under
// the final short drain/control/material/traffic health fences.
func (r *Repository) ConfirmHTTP(ctx context.Context, candidate *HTTPEvaluationCandidate, id string, materials *httpcredentials.Service, receipt func(string, func() bool) bool) error {
	if candidate == nil || candidate.repository != r || candidate.id != id || receipt == nil || !candidate.used.CompareAndSwap(false, true) {
		return ErrAdmissionUnavailable
	}
	release, err := r.authority.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	err = r.view(ctx, func(tx *sql.Tx) error {
		if err := verifyCurrentBindingTx(ctx, tx, candidate.lease.binding); err != nil {
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
		return err
	}
	detach := func() error {
		registry := r.authority
		registry.mu.Lock()
		defer registry.mu.Unlock()
		if registry.draining.Load() || ctx.Err() != nil {
			return ErrAdmissionUnavailable
		}
		confirmed := r.store.ConfirmHealthy(func() bool {
			return receipt(candidate.evidence, func() bool {
				if ctx.Err() != nil || !candidate.lease.phase.CompareAndSwap(uint32(leasePending), uint32(leaseAdmitted)) {
					return false
				}
				delete(registry.leases, candidate.lease)
				return true
			})
		})
		if !confirmed {
			return ErrAdmissionUnavailable
		}
		return nil
	}
	if candidate.material != nil {
		return materials.ConfirmMaterial(ctx, candidate.material.Credential, candidate.material.Generation, detach)
	}
	return detach()
}
