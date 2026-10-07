package composition

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/credentialauthority"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// MaintainSecrets never starts a serving graph. Verification is immutable and
// never constructs a native provider; migration and cleanup require fresh consent.
func MaintainSecrets(ctx context.Context, root, operation string, clock Clock, approval storage.StoppedApproval) (keyring.MigrationResult, error) {
	return maintainSecrets(ctx, root, operation, clock, approval, productionProvider)
}

func maintainSecrets(ctx context.Context, root, operation string, clock Clock, approval storage.StoppedApproval, factory func(string) (*keyring.Provider, error)) (result keyring.MigrationResult, err error) {
	if operation != "migrate-secrets" && operation != "verify-secrets" && operation != "cleanup-native-secrets" {
		return result, keyring.ErrMigrationIncomplete
	}
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, owner.Close()) }()
	if err = gatewaypaths.RequireNoKeyRotation(owner); err != nil {
		return result, err
	}
	snapshot, err := storage.InspectMaintenance(ctx, owner, nil)
	if err != nil {
		return result, err
	}
	if snapshot.Marked {
		return result, storage.ErrStorageLatched
	}
	if err = storage.ApproveStopped(ctx, owner, []storage.StoppedApproval{approval}); err != nil {
		return result, err
	}
	if err = snapshot.Revalidate(ctx, owner); err != nil {
		return result, err
	}
	if operation == "verify-secrets" {
		_, err = storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error { return verifyMigrationTx(ctx, tx, owner) })
		return result, err
	}
	store, err := storage.Open(ctx, owner)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	native, err := factory(snapshot.Identity.InstallationID)
	if err != nil {
		return result, err
	}
	if operation == "migrate-secrets" {
		result, err = keyring.MigrateLegacy(ctx, owner, store, native, clock, inspectMigrationMaterialTx)
		if err != nil {
			return result, err
		}
		err = store.View(ctx, func(tx *sql.Tx) error { return verifyMigrationTx(ctx, tx, owner) })
		return result, err
	}
	result, err = keyring.CleanupNative(ctx, store, native, func(ctx context.Context, tx *sql.Tx) error { return verifyMigrationTx(ctx, tx, owner) })
	if err != nil {
		return result, err
	}
	err = store.Mutate(ctx, func(tx *sql.Tx) error {
		return audit.MutationTx(audit.WithOffline(ctx), tx, clock.Now(), "keyring", "cleanup", contract.AuditTarget{Type: "installation", ID: snapshot.Identity.InstallationID})
	})
	return result, err
}

func inspectMigrationMaterialTx(ctx context.Context, tx *sql.Tx, kind keyring.RecordKind, handle keyring.Handle, payload []byte) error {
	// Bidirectional tuple validation preserves each domain's metadata, while the
	// keyring transaction independently rechecks exact authority and fences.
	for _, verify := range []func(context.Context, *sql.Tx) error{servers.VerifyBackupCredentialsTx, httpcredentials.VerifyBackupTx, gitcredentials.VerifyEncryptedBackupTx, httpca.VerifyBackupTx} {
		if err := verify(ctx, tx); err != nil {
			return err
		}
	}
	switch kind {
	case keyring.RecordStaticCredential, keyring.RecordOAuthClient, keyring.RecordOAuthTokens:
		return credentialauthority.VerifyMigrationMaterialTx(ctx, tx, kind, handle, payload)
	case keyring.RecordHTTPCredential:
		return httpcredentials.VerifyMigrationMaterialTx(ctx, tx, handle, payload)
	case keyring.RecordGitCredential:
		return gitcredentials.VerifyMigrationMaterialTx(ctx, tx, handle, payload)
	case keyring.RecordHTTPCA:
		return httpca.VerifyBackupMaterialTx(ctx, tx, string(handle), payload)
	default:
		return keyring.ErrMigrationIncomplete
	}
}

func verifyMigrationTx(ctx context.Context, tx *sql.Tx, owner *gatewaypaths.Ownership) error {
	for _, verify := range []func(context.Context, *sql.Tx) error{credentialauthority.VerifyMigrationDependenciesTx, httpcredentials.VerifyMigrationDependenciesTx, gitcredentials.VerifyMigrationDependenciesTx, httpca.VerifyBackupTx} {
		if err := verify(ctx, tx); err != nil {
			return keyring.ErrMigrationIncomplete
		}
	}
	ca, err := httpca.InspectTx(ctx, tx)
	if err != nil || ca.Unsettled || ((ca.Present || ca.Revision != "0") && !ca.Selected) {
		return keyring.ErrMigrationIncomplete
	}
	_, err = keyring.VerifyBackupCustodyTx(ctx, tx, owner, func(kind keyring.RecordKind, handle keyring.Handle, payload []byte) error {
		// Retired encrypted generations need cryptographic authentication but have no
		// active domain metadata. They never license native fallback or resurrection.
		selected, err := keyring.SelectedGenerationTx(ctx, tx, handle)
		if err != nil {
			return err
		}
		if !selected {
			return nil
		}
		return inspectMigrationMaterialTx(ctx, tx, kind, handle, payload)
	})
	if err != nil {
		return keyring.ErrMigrationIncomplete
	}
	return nil
}
