package httpcredentials

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

// PolicyCredentialTx reads only safe metadata on the authority owner's coherent
// snapshot. Availability is a durable binding fact, not a keyring material probe.
func PolicyCredentialTx(ctx context.Context, tx *sql.Tx, id string) (httppolicy.Credential, error) {
	rec, err := readTx(ctx, tx, id)
	if err != nil {
		return httppolicy.Credential{}, err
	}
	if rec.deleted {
		return httppolicy.Credential{}, ErrNotFound
	}
	def, err := Normalize(rec.Definition)
	if err != nil || def != rec.Definition {
		return httppolicy.Credential{}, ErrInvalid
	}
	ref, err := revisionRef(rec)
	if err != nil {
		return httppolicy.Credential{}, err
	}
	if err := availabilityTx(ctx, tx, &rec); err != nil {
		return httppolicy.Credential{}, err
	}
	return rec.PolicyCredential(ref, rec.Available), nil
}

// CheckPolicyReferenceTx is the same whole-scope check used by repository edits.
// It never acquires a writer or resolves secret material.
func CheckPolicyReferenceTx(ctx context.Context, tx *sql.Tx, id string, policy httppolicy.Policy) error {
	if policy.CredentialID() != id {
		return ErrReferenced
	}
	credential, err := PolicyCredentialTx(ctx, tx, id)
	if err != nil {
		return err
	}
	contained, err := httppolicy.CredentialContains(credential, policy)
	if err != nil || !contained {
		return ErrReferenced
	}
	return nil
}
