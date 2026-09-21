package invocation

import (
	"context"
	"database/sql"
	"errors"
	"net/url"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

// RestoreTraffic publishes validated evidence under a fresh history generation.
// It does not select control metadata, copy pins, or reconstruct execution work.
func RestoreTraffic(ctx context.Context, owner *gatewaypaths.Ownership, source, installation, oldGeneration, generation string, config TrafficConfig) error {
	if err := VerifyTrafficFile(ctx, source, installation, oldGeneration, config); err != nil {
		return err
	}
	uri := &url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro&immutable=1"}
	database, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	traffic, err := openTrafficStage(ctx, owner, installation, generation, config, true, nil, func(ctx context.Context, destination *sql.DB) error {
		tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		err = extractTraffic(ctx, tx, destination, config, config.RetainedRecords)
		return errors.Join(err, tx.Rollback())
	})
	if err != nil {
		return err
	}
	return traffic.Close()
}
