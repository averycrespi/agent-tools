package keyring

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// RotationResult contains safe disposition only, never key material.
type RotationResult struct {
	Identity    storage.Identity
	Retained    string
	Disposition string
}

// RotateStoppedMasterKey owns one stopped transaction and its key publication.
// Recovery only reconciles a previously armed operation; it never re-encrypts.
func RotateStoppedMasterKey(ctx context.Context, owner *gatewaypaths.Ownership, clock Clock, recoverPending bool, approval storage.StoppedApproval) (RotationResult, error) {
	return rotateStoppedMasterKey(ctx, owner, clock, recoverPending, approval, nil)
}

func rotateStoppedMasterKey(ctx context.Context, owner *gatewaypaths.Ownership, clock Clock, recoverPending bool, approval storage.StoppedApproval, fault func(string) error) (result RotationResult, resultErr error) {
	if clock == nil || owner == nil {
		return result, ErrCustodyUnavailable
	}
	changed := false
	defer func() {
		if resultErr != nil && changed {
			resultErr = &storage.OperationError{Effect: "uncertain", Cause: resultErr}
		}
	}()
	step := func(point string) error {
		if fault != nil {
			return fault(point)
		}
		return nil
	}
	if recoverPending {
		return recoverMasterKey(ctx, owner, approval, step)
	}
	if err := gatewaypaths.RequireNoKeyRotation(owner); err != nil {
		return result, err
	}
	snapshot, err := storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
		_, err := VerifyBackupCustodyTx(ctx, tx, owner, nil)
		return err
	})
	if err != nil {
		return result, err
	}
	if snapshot.Marked {
		return result, storage.ErrStorageLatched
	}
	if err := storage.ApproveStopped(ctx, owner, []storage.StoppedApproval{approval}); err != nil {
		return result, err
	}
	if err := snapshot.Revalidate(ctx, owner); err != nil {
		return result, err
	}
	oldKey, err := gatewaypaths.DurableMasterKey(owner)
	if err != nil {
		return result, ErrCustodyUnavailable
	}
	defer clear(oldKey)
	newKey := make([]byte, 32)
	defer clear(newKey)
	if _, err = rand.Read(newKey); err != nil {
		return result, ErrCustodyUnavailable
	}
	material := gatewaypaths.KeyRotationMaterial{InstallationID: snapshot.Identity.InstallationID, OldKey: oldKey, NewKey: newKey}
	store, err := storage.Open(ctx, owner)
	if err != nil {
		return result, err
	}
	closed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, store.Close())
		}
	}()
	// Every attempt uses a fresh key. If the transaction is uncertain, no path
	// retries encryption under its retained candidate, even after rollback.
	changed = true
	if err := gatewaypaths.PrepareKeyRotation(owner, material); err != nil {
		return result, err
	}
	if err := step("armed"); err != nil {
		return result, err
	}
	var count int64
	if err := store.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM secret_generations`).Scan(&count)
	}); err != nil {
		return result, err
	}
	if err := RewrapCustody(ctx, store, oldKey, newKey, count, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`); err != nil {
			return err
		}
		if err := audit.MutationTx(audit.WithOffline(ctx), tx, clock.Now(), "keyring", "rotate", contract.AuditTarget{Type: "installation", ID: snapshot.Identity.InstallationID}); err != nil {
			return err
		}
		return step("before_database_commit")
	}); err != nil {
		return result, err
	}
	if err := step("database_commit"); err != nil {
		return result, err
	}
	if err := store.Checkpoint(ctx); err != nil {
		return result, err
	}
	result.Identity, err = store.Identity(ctx)
	if err != nil {
		return result, err
	}
	if err := store.Close(); err != nil {
		return result, err
	}
	closed = true
	if err := step("database_closed"); err != nil {
		return result, err
	}
	if err := gatewaypaths.PublishRotatedKey(owner, material); err != nil {
		return result, err
	}
	if err := step("key_published"); err != nil {
		return result, err
	}
	// Verify the selected key against every committed generation before unblocking
	// startup. Reconciliation repeats these reads, never the encryption transaction.
	if _, err := storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
		_, err := VerifyBackupCustodyTx(ctx, tx, owner, nil)
		return err
	}); err != nil {
		return result, err
	}
	result.Retained, err = gatewaypaths.RetainKeyRotation(owner, material)
	if err != nil {
		return result, err
	}
	result.Disposition = "rotated"
	return result, step("retained")
}

func recoverMasterKey(ctx context.Context, owner *gatewaypaths.Ownership, approval storage.StoppedApproval, step func(string) error) (result RotationResult, resultErr error) {
	material, err := gatewaypaths.ReadKeyRotation(owner)
	if err != nil {
		return result, errors.Join(gatewaypaths.ErrKeyRotationPending, err)
	}
	defer material.Clear()
	var selected BackupCustody
	snapshot, err := storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
		var err error
		selected, err = InspectBackupCustodyTx(ctx, tx)
		if err != nil {
			return err
		}
		key := material.OldKey
		if selected.KeyID == masterKeyID(material.NewKey) {
			key = material.NewKey
		} else if selected.KeyID != masterKeyID(material.OldKey) {
			return ErrCustodyUnavailable
		}
		_, err = VerifyRecoveryCustodyTx(ctx, tx, key, nil)
		return err
	})
	if err != nil {
		return result, err
	}
	if snapshot.Marked {
		return result, storage.ErrStorageLatched
	}
	if snapshot.Identity.InstallationID != material.InstallationID {
		return result, ErrCustodyUnavailable
	}
	current, err := gatewaypaths.DurableMasterKey(owner)
	if err != nil {
		return result, ErrCustodyUnavailable
	}
	defer clear(current)
	committed := selected.KeyID == masterKeyID(material.NewKey)
	if !bytes.Equal(current, material.OldKey) && (!committed || !bytes.Equal(current, material.NewKey)) {
		return result, ErrCustodyUnavailable
	}
	if err := storage.ApproveStopped(ctx, owner, []storage.StoppedApproval{approval}); err != nil {
		return result, err
	}
	if err := snapshot.Revalidate(ctx, owner); err != nil {
		return result, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = &storage.OperationError{Effect: "uncertain", Cause: resultErr}
		}
	}()
	result.Disposition = "aborted-before-commit"
	if committed {
		if err := gatewaypaths.PublishRotatedKey(owner, material); err != nil {
			return result, err
		}
		result.Disposition = "completed-committed-rotation"
	}
	if err := step("recovery_key_settled"); err != nil {
		return result, err
	}
	result.Retained, err = gatewaypaths.RetainKeyRotation(owner, material)
	result.Identity = snapshot.Identity
	return result, err
}

// RewrapCustody transforms complete encrypted custody without changing handles
// or authority. The target budget must already be durably reserved in the live
// installation, or belong to a fresh never-reused rotation candidate.
func RewrapCustody(ctx context.Context, store *storage.Store, oldKey, newKey []byte, targetBudget int64, finish func(*sql.Tx) error) error {
	if len(oldKey) != 32 || len(newKey) != 32 || bytes.Equal(oldKey, newKey) || targetBudget < 0 || targetBudget > 4294967296 {
		return ErrCustodyUnavailable
	}
	oldID, newID := masterKeyID(oldKey), masterKeyID(newKey)
	oldBlock, err := aes.NewCipher(oldKey)
	if err != nil {
		return ErrCustodyUnavailable
	}
	oldAEAD, err := cipher.NewGCMWithRandomNonce(oldBlock)
	if err != nil {
		return ErrCustodyUnavailable
	}
	newBlock, err := aes.NewCipher(newKey)
	if err != nil {
		return ErrCustodyUnavailable
	}
	newAEAD, err := cipher.NewGCMWithRandomNonce(newBlock)
	if err != nil {
		return ErrCustodyUnavailable
	}
	return store.Mutate(ctx, func(tx *sql.Tx) error {
		custody, err := VerifyRecoveryCustodyTx(ctx, tx, oldKey, nil)
		if err != nil || custody.KeyID != oldID {
			return errors.Join(ErrCustodyUnavailable, err)
		}
		var installation string
		if err := tx.QueryRowContext(ctx, `SELECT installation_id FROM gateway_meta WHERE singleton=1`).Scan(&installation); err != nil {
			return err
		}
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM secret_generations`).Scan(&count); err != nil {
			return err
		}
		if count > targetBudget {
			return ErrCustodyUnavailable
		}
		after := ""
		for i := int64(0); i < count; i++ {
			var handle, recordOwner, kind string
			var sealed []byte
			if err := tx.QueryRowContext(ctx, `SELECT handle,owner,kind,substr(ciphertext,1,262173) FROM secret_generations WHERE handle>? ORDER BY handle LIMIT 1`, after).Scan(&handle, &recordOwner, &kind, &sealed); err != nil {
				return err
			}
			namespace, err := NewNamespace(installation, recordOwner, RecordKind(kind))
			if err != nil {
				return ErrIncompleteGeneration
			}
			parsed, err := ParseHandle(handle)
			if err != nil {
				return ErrIncompleteGeneration
			}
			plaintext, err := oldAEAD.Open(nil, nil, sealed, custodyBinding(namespace, parsed, oldID))
			if err != nil {
				return ErrIncompleteGeneration
			}
			replacement := newAEAD.Seal(nil, nil, plaintext, custodyBinding(namespace, parsed, newID))
			clear(plaintext)
			if _, err := tx.ExecContext(ctx, `UPDATE secret_generations SET key_id=?,ciphertext=? WHERE handle=?`, newID, replacement, handle); err != nil {
				return err
			}
			after = handle
		}
		if _, err := tx.ExecContext(ctx, `UPDATE secret_custody SET key_id=?,encryptions=? WHERE singleton=1 AND key_id=?`, newID, targetBudget, oldID); err != nil {
			return err
		}
		if finish != nil {
			return finish(tx)
		}
		return nil
	})
}
