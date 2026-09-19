package invocation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// MigrateTraffic runs only under stopped installation ownership, or during fresh
// initialization before any authority is published. It retains the legacy rows
// and unselected stages on failure; there is no serve-time backfill or retry.
func MigrateTraffic(ctx context.Context, owner *gatewaypaths.Ownership, control *storage.Store, generation string, config TrafficConfig) error {
	return migrateTraffic(ctx, owner, control, generation, config, nil)
}

func migrateTraffic(ctx context.Context, owner *gatewaypaths.Ownership, control *storage.Store, generation string, config TrafficConfig, fault func(string) error) error {
	if owner == nil || control == nil || !config.valid() || !validOpaqueInvocationID(generation) {
		return ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if control.Latched() {
		return storage.ErrStorageLatched
	}
	selected, err := control.SelectedTraffic(ctx)
	if err != nil {
		return err
	}
	if selected != "" {
		return ErrInvalidState
	}
	identity, err := control.Identity(ctx)
	if err != nil {
		return err
	}
	traffic, err := openTrafficStage(ctx, owner, identity.InstallationID, generation, config, true, nil, func(ctx context.Context, destination *sql.DB) error {
		return control.View(ctx, func(source *sql.Tx) error { return extractTraffic(ctx, source, destination, config, invocationLimit()) })
	})
	if err != nil {
		return err
	}
	if err = traffic.Close(); err != nil {
		return err
	}
	if fault != nil {
		if err = fault("published"); err != nil {
			return err
		}
	}
	if err = control.SelectTraffic(ctx, "", generation); err != nil {
		return err
	}
	if err = ClearLegacyTraffic(ctx, control); err != nil {
		return err
	}
	if fault != nil {
		return fault("selected")
	}
	return nil
}

// ClearLegacyTraffic runs on a stopped replacement only, after its matching
// traffic generation is durable. The original control generation is retained.
func ClearLegacyTraffic(ctx context.Context, control *storage.Store) error {
	return control.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM invocations`)
		return err
	})
}

func extractTraffic(ctx context.Context, source *sql.Tx, destination *sql.DB, config TrafficConfig, sourceLimit int64) (result error) {
	tx, err := destination.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, tx.Rollback())
		}
	}()
	var high int64
	if err = source.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='invocations'),0)`).Scan(&high); err != nil {
		return err
	}
	rows, err := source.QueryContext(ctx, invocationSelect+` ORDER BY insertion_sequence LIMIT ?`, sourceLimit+1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, rows.Close()) }()
	var count, previous, bytes int64
	for rows.Next() {
		record, err := scanInvocation(rows)
		if err != nil {
			return err
		}
		count++
		if count > sourceLimit || count > config.RetainedRecords || record.Sequence <= previous || record.Sequence > high || !validStoredInvocation(record) {
			return ErrInvalidState
		}
		previous = record.Sequence
		envelope, details, _ := storedEvidence(record)
		prepared := PreparedAdmission{Identity: envelope.Identity, admission: Admission{Admission: envelope.Admission, MCP: details}}
		values, err := admissionSQLValues(prepared)
		if err != nil {
			return err
		}
		values = append([]any{record.Sequence}, values...)
		values = append(values, record.CompletedAt, record.TerminalClass)
		if _, err = tx.ExecContext(ctx, `INSERT INTO invocations
		(insertion_sequence,id,principal_id,credential_id,credential_fingerprint,
		credential_revision,admitted_at,admission_class,requested_name,redacted_arguments,
		server_id,tool_id,upstream_name,descriptor_revision,descriptor_fingerprint,
		decision,authorization_revision,evaluated_at,grant_id,completed_at,terminal_class)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, values...); err != nil {
			return err
		}
		charge := trafficCharge(prepared)
		bytes += charge
		if bytes > trafficPages(config)*trafficPageSize/4 {
			return ErrTrafficCapacity
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO traffic_sizes VALUES (?,?)`, record.InvocationID, charge); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if high < count {
		return ErrInvalidState
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name='invocations'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name,seq) VALUES('invocations',?)`, high); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE traffic_meta SET high_water=?,pruning=? WHERE singleton=1`, high, high-count); err != nil {
		return err
	}
	return tx.Commit()
}
