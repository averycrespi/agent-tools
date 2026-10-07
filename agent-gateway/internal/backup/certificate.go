package backup

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// The selected certificate lives atomically with its signing identity in SQLite.
// The managed PEM is a derived export, published only after database installation;
// publication failure never implies that the database replacement rolled back.
func inspectRestoreCertificate(ctx context.Context, owner *gatewaypaths.Ownership, artifact artifactMetadata) (previous, certificate []byte, err error) {
	if artifact.Format != 4 {
		return nil, nil, nil
	}
	_, err = storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
		inspection, e := httpca.InspectTx(ctx, tx)
		previous = inspection.Certificate
		return e
	})
	if err != nil {
		return nil, nil, err
	}
	err = storage.ViewBackup(ctx, filepath.Join(owner.Layout().Backups, artifact.ID, databaseFile), func(tx *sql.Tx) error {
		inspection, e := httpca.InspectTx(ctx, tx)
		certificate = inspection.Certificate
		return e
	})
	if err != nil {
		return nil, nil, err
	}
	if len(certificate) != 0 {
		err = gatewaypaths.CheckCertificateDestination(filepath.Join(owner.Layout().Root, gatewaypaths.PublicCertificateName), previous)
	}
	return previous, certificate, err
}
