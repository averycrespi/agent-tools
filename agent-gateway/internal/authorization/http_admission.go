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
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

// HTTPEvaluationCandidate seals one coherent evaluation without retaining an
// authority lock, submitted URL or executable callback across persistence.
type HTTPEvaluationCandidate struct {
	repository       *Repository
	lease            *Lease
	id, revision     string
	request          context.Context
	material         *HTTPMaterial
	used             atomic.Bool
	opaqueOrigin     string
	opaqueRegistered atomic.Bool
	opaqueReleased   atomic.Bool
}

// HTTPExecution contains the selected executable facts, not a capture DTO.
type HTTPExecution struct {
	EvaluatedAt    time.Time
	Allowed        bool
	Transport      contract.HTTPTransport
	Reason         contract.HTTPDecisionReason
	PrivateNetwork bool
	Material       *HTTPMaterial
}

type HTTPMaterial struct {
	Credential contract.HTTPRevisionRef
	Generation string
}

type HTTPEvaluation struct {
	Execution HTTPExecution
	Evidence  contract.HTTPTrafficAdmission
	Candidate *HTTPEvaluationCandidate
}

// HTTPAdmissionContext is engine-owned evidence, never part of access-test input.
type HTTPAdmissionContext struct {
	Rejection *contract.HTTPRejection
	Connect   *contract.HTTPConnectContext
}

func (r *Repository) EvaluateHTTPAdmission(ctx context.Context, lease *Lease, id, admittedAt string, in HTTPAccessInput, facts httppolicy.AddressFacts, contexts ...HTTPAdmissionContext) (out HTTPEvaluation, err error) {
	if len(contexts) > 1 {
		return out, ErrInvalidInput
	}
	var metadata HTTPAdmissionContext
	if len(contexts) == 1 {
		metadata = contexts[0]
	}
	if metadata.Rejection != nil && (!metadata.Rejection.Valid() || in.URL != "" || in.Method != "" || in.Connect != nil) {
		return out, ErrInvalidInput
	}
	// CONNECT correlation is capture-only. The recorder validates its identity
	// and coordinates; malformed/colliding metadata must not alter live authority.
	if lease == nil || in.PrincipalID != lease.binding.PrincipalID {
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
			if err != nil {
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
			if metadata.Connect != nil {
				copy := *metadata.Connect
				evidence.Connect = &copy
			}
			if metadata.Rejection != nil {
				copy := *metadata.Rejection
				evidence.Rejection = &copy
			}
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
			profile, err := gitRoutingProfileTx(ctx, tx)
			if err != nil {
				return err
			}
			if slices.Contains(profile.Origins, "https://"+target.destination.Authority()) {
				if target.connect && decision.Transport == contract.HTTPTransportTunnel {
					return ErrAuthorizationUnavailable
				}
				if !target.connect && target.request.Scheme() == "https" {
					repositories, err := readGitRepositoriesTx(ctx, tx)
					if err != nil {
						return err
					}
					_, _, shaped, _ := gitwire.Route(target.request, gitLocators(repositories)...)
					// Classification can precede profile activation. Refuse under
					// this same authority snapshot rather than rerouting or using
					// an HTTP candidate at the newer policy revision.
					if shaped {
						return ErrAuthorizationUnavailable
					}
				}
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
			out.Execution = HTTPExecution{EvaluatedAt: now, Allowed: decision.Allowed, Transport: decision.Transport, Reason: decision.Reason, PrivateNetwork: decision.PrivateGrant != nil}
			var material *HTTPMaterial
			if evidence.Material != nil {
				material = &HTTPMaterial{Credential: evidence.Material.Credential, Generation: evidence.Material.Generation}
				copy := *material
				out.Execution.Material = &copy
			}
			if decision.Allowed {
				out.Candidate = &HTTPEvaluationCandidate{repository: r, lease: lease, id: id, revision: binding.AuthorizationRevision, request: ctx, material: material}
				if target.connect && decision.Transport == contract.HTTPTransportTunnel {
					out.Candidate.opaqueOrigin = "https://" + target.destination.Authority()
				}
			}
			return nil
		})
	})
	if err != nil {
		return HTTPEvaluation{}, err
	}
	return out, nil
}

// ConfirmHTTP consumes the sealed candidate under the drain/control/material
// fences. Original request cancellation cannot be replaced by a fresh context.
func (r *Repository) ConfirmHTTP(ctx context.Context, candidate *HTTPEvaluationCandidate, id string, materials *httpcredentials.Service) error {
	if candidate == nil || candidate.repository != r || candidate.id != id || !candidate.used.CompareAndSwap(false, true) {
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
		if registry.draining.Load() || ctx.Err() != nil || candidate.request.Err() != nil {
			return ErrAdmissionUnavailable
		}
		confirmed := r.store.ConfirmHealthy(func() bool {
			if ctx.Err() != nil || candidate.request.Err() != nil || !candidate.lease.phase.CompareAndSwap(uint32(leasePending), uint32(leaseAdmitted)) {
				return false
			}
			delete(registry.leases, candidate.lease)
			if candidate.opaqueOrigin != "" {
				registry.opaqueGitOrigins[candidate.opaqueOrigin]++
				candidate.opaqueRegistered.Store(true)
			}
			return true
		})
		if !confirmed {
			return ErrAdmissionUnavailable
		}
		return nil
	}
	if candidate.material != nil {
		if materials == nil {
			return ErrAdmissionUnavailable
		}
		return materials.ConfirmMaterial(ctx, candidate.material.Credential, candidate.material.Generation, detach)
	}
	return detach()
}
