package authorization

import (
	"context"
	"database/sql"
	"slices"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// GitRejection binds a classified pre-dispatch rejection to the authenticated
// principal without retaining any unsupported client coordinates or controls.
func (r *Repository) GitRejection(ctx context.Context, lease *Lease, id, admittedAt, reason string) (out contract.GitTrafficAdmission, err error) {
	if lease == nil || !slices.Contains([]string{"unsupported", "repository_unavailable", "destination_unavailable"}, reason) {
		return out, ErrInvalidInput
	}
	err = r.WithAdmission(ctx, lease, func(admission *Admission) error {
		return r.view(ctx, func(tx *sql.Tx) error {
			binding, e := admission.VerifyBindingOnlyTx(ctx, tx)
			if e != nil {
				return e
			}
			profile, e := gitRoutingProfileTx(ctx, tx)
			if e != nil {
				return e
			}
			out = contract.GitTrafficAdmission{ID: id, AdmittedAt: admittedAt, EvaluatedAt: binding.EvaluatedAt, Principal: contract.GitRevisionRef{ID: lease.binding.PrincipalID, Revision: lease.binding.PrincipalRevision}, AgentCredential: contract.GitRevisionRef{ID: lease.binding.CredentialID, Revision: lease.binding.CredentialRevision}, AuthorizationRevision: binding.AuthorizationRevision, ProfileRevision: profile.Revision, Operation: "invalid", Rejection: reason}
			return nil
		})
	})
	return
}
