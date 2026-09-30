package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

// GitMaterialConfirmer avoids reversing the credential owner's dependency on
// authorization. It performs metadata-only confirmation under authority.
type GitMaterialConfirmer interface {
	ConfirmMaterial(context.Context, contract.GitRevisionRef, string, func() error) error
}

type GitEvaluationCandidate struct {
	repository             *Repository
	lease                  *Lease
	request                *gitwire.Request
	id, revision, evidence string
	material               *contract.GitTrafficMaterial
	used                   atomic.Bool
}
type GitEvaluation struct {
	Evidence  contract.GitTrafficAdmission
	Candidate *GitEvaluationCandidate
}

func (r *Repository) EvaluateGitAdmission(ctx context.Context, lease *Lease, id, admittedAt string, request *gitwire.Request, facts httppolicy.AddressFacts) (out GitEvaluation, err error) {
	if lease == nil || request == nil || !validOpaqueID(id) {
		return out, ErrInvalidInput
	}
	admitted, err := time.Parse(time.RFC3339Nano, admittedAt)
	if err != nil || formatAuthorizationTime(admitted) != admittedAt {
		return out, ErrInvalidInput
	}
	err = r.WithAdmission(ctx, lease, func(admission *Admission) error {
		return r.view(ctx, func(tx *sql.Tx) error {
			binding, e := admission.VerifyBindingOnlyTx(ctx, tx)
			if e != nil {
				return e
			}
			now, e := time.Parse(time.RFC3339Nano, binding.EvaluatedAt)
			if e != nil || now.Before(admitted) {
				return ErrInvalidInput
			}
			supplied := request.Repository()
			repo, e := gitRepositoryTx(ctx, tx, supplied.ID)
			if e != nil {
				return e
			}
			profile, e := gitRoutingProfileTx(ctx, tx)
			if e != nil {
				return e
			}
			if repo.deleted || repo.resource.Revision != supplied.Revision || repo.resource.AliasRevision != supplied.AliasRevision || profile.Revision != request.ProfileRevision() || !slices.Contains(profile.Origins, gitOrigin(repo.resource.URL)) {
				return ErrAuthorizationUnavailable
			}
			base, _, shaped, e := gitwire.Route(request.Target(), append([]string{repo.resource.URL}, repo.resource.Aliases...)...)
			if e != nil || !shaped || (base != repo.resource.URL && !slices.Contains(repo.resource.Aliases, base)) {
				return ErrAuthorizationUnavailable
			}
			grants, e := readGitGrantsTx(ctx, tx, now)
			if e != nil {
				return e
			}
			applicable := []gitpolicy.Grant{}
			capable := false
			for _, g := range grants {
				if g.PrincipalID == lease.binding.PrincipalID && g.RepositoryID == supplied.ID && g.State == contract.GrantActive {
					applicable = append(applicable, gitpolicy.Grant{Policy: g.Policy})
					capable = capable || g.Policy.Read && len(g.Policy.Refs) > 0
				}
			}
			allowed, e := gitpolicy.Evaluate(applicable, request.Actions())
			if e != nil {
				return ErrInvalidInput
			}
			if request.PushCapable() {
				allowed = allowed && capable
			}
			evaluator, _, e := httpEvaluatorTx(ctx, tx, lease.binding.PrincipalID, now)
			if e != nil {
				return e
			}
			network, private, e := evaluator.GitDestination(request.Target(), facts)
			if e != nil {
				return e
			}
			allowed = allowed && network
			evidence := contract.GitTrafficAdmission{ID: id, AdmittedAt: admittedAt, EvaluatedAt: binding.EvaluatedAt, Principal: contract.GitRevisionRef{ID: lease.binding.PrincipalID, Revision: lease.binding.PrincipalRevision}, AgentCredential: contract.GitRevisionRef{ID: lease.binding.CredentialID, Revision: lease.binding.CredentialRevision}, Repository: contract.GitRevisionRef{ID: supplied.ID, Revision: supplied.Revision}, AliasRevision: supplied.AliasRevision, ProfileRevision: profile.Revision, AuthorizationRevision: binding.AuthorizationRevision, Operation: request.Operation(), Commands: len(request.Actions()), Allowed: allowed, PrivateGrant: private}
			if allowed && repo.resource.CredentialID != nil {
				material, e := gitMaterialTx(ctx, tx, *repo.resource.CredentialID)
				if e != nil {
					return e
				}
				evidence.Material = &material
			}
			out.Evidence = evidence
			if allowed {
				encoded, e := json.Marshal(evidence)
				if e != nil || len(encoded) > contract.GitTrafficAdmissionBytes {
					return ErrAuthorizationUnavailable
				}
				var material *contract.GitTrafficMaterial
				if evidence.Material != nil {
					copy := *evidence.Material
					material = &copy
				}
				out.Candidate = &GitEvaluationCandidate{repository: r, lease: lease, request: request, id: id, revision: binding.AuthorizationRevision, evidence: string(encoded), material: material}
			}
			return nil
		})
	})
	if err != nil {
		return GitEvaluation{}, err
	}
	return out, nil
}

// Metadata is owned by Git credential SQL, but authorization reads the selected
// revision/generation in the same snapshot as policy, without secret access.
func gitMaterialTx(ctx context.Context, tx *sql.Tx, id string) (out contract.GitTrafficMaterial, err error) {
	out.Credential.ID = id
	var available bool
	err = tx.QueryRowContext(ctx, `SELECT revision,material_revision,deleted=0 AND handle IS NOT NULL AND EXISTS(SELECT 1 FROM keyring_authorities a WHERE a.owner=git_credentials.id AND a.kind='git_credential' AND a.handle=git_credentials.handle AND a.revision=git_credentials.material_revision AND NOT EXISTS(SELECT 1 FROM keyring_authority_fences f WHERE f.owner=a.owner AND f.kind=a.kind)) FROM git_credentials WHERE id=?`, id).Scan(&out.Credential.Revision, &out.Generation, &available)
	if err == nil && (!available || !gitpolicy.ValidRevision(out.Credential.Revision) || !gitpolicy.ValidRevision(out.Generation)) {
		err = ErrAdmissionUnavailable
	}
	return
}

// ConfirmGit consumes the exact request-local candidate, not just equal retained
// summaries. No re-evaluation and no authority/storage ownership over I/O.
func (r *Repository) ConfirmGit(ctx context.Context, candidate *GitEvaluationCandidate, id string, request *gitwire.Request, materials GitMaterialConfirmer, receipt func(string, func() bool) bool) error {
	if candidate == nil || candidate.repository != r || candidate.id != id || receipt == nil || !candidate.used.CompareAndSwap(false, true) || candidate.request != request {
		return ErrAdmissionUnavailable
	}
	release, err := r.authority.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	err = r.view(ctx, func(tx *sql.Tx) error {
		if e := verifyCurrentBindingTx(ctx, tx, candidate.lease.binding); e != nil {
			return e
		}
		revision, e := authorizationRevisionTx(ctx, tx)
		if e != nil {
			return e
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
		if materials == nil {
			return ErrAdmissionUnavailable
		}
		return materials.ConfirmMaterial(ctx, candidate.material.Credential, candidate.material.Generation, detach)
	}
	return detach()
}
