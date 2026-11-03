package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

// ReadDiagnosticMetadata reads bounded metadata from a closed generation under
// existing stopped ownership. It does not validate integrity, hash the database,
// recover SQLite, consult custody, or create sidecars. Its facts cannot authorize
// mutation; maintenance retains its independent full validation.
func ReadDiagnosticMetadata(ctx context.Context, owner *gatewaypaths.Ownership, view func(*sql.Tx) error) (identity Identity, resultErr error) {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return identity, err
	}
	if err := gatewaypaths.InspectRoot(layout.Root); err != nil {
		return identity, err
	}
	if err := gatewaypaths.ValidateOwnerOnlyFile(layout.Database); err != nil {
		return identity, err
	}
	if err := RequireClosedGeneration(layout.Database); err != nil {
		return identity, err
	}
	uri := &url.URL{Scheme: "file", Path: layout.Database, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return identity, err
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return identity, err
	}
	defer func() { _ = tx.Rollback() }()
	var application, schema int
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT application_id FROM pragma_application_id),(SELECT user_version FROM pragma_user_version),installation_id,revision FROM gateway_meta WHERE singleton=1`).Scan(&application, &schema, &identity.InstallationID, &revision); err != nil {
		return Identity{}, fmt.Errorf("read diagnostic identity: %w", err)
	}
	if application != ApplicationID || schema != CurrentSchema || !installationIDPattern.MatchString(identity.InstallationID) || revision < 0 {
		return Identity{}, ErrInvalidDatabase
	}
	identity.SchemaVersion, identity.Revision = schema, uint64(revision)
	if err := newMutationMarker(layout, nil).validateHistorical(identity.InstallationID); err != nil {
		return identity, errors.Join(ErrStorageLatched, err)
	}
	if view != nil {
		if err := view(tx); err != nil {
			return identity, err
		}
	}
	return identity, nil
}
