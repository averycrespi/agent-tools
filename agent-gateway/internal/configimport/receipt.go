package configimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configexport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
)

// VerifyReceiptTx reconciles a committed import, never recreates its effects.
func VerifyReceiptTx(ctx context.Context, tx *sql.Tx, inputDigest string) error {
	var expectedInput, expectedStage string
	if err := tx.QueryRowContext(ctx, `SELECT input_digest,staged_digest FROM configuration_import WHERE singleton=1`).Scan(&expectedInput, &expectedStage); err != nil {
		return err
	}
	if expectedInput != inputDigest {
		return ErrInvalid
	}
	actual, err := stagedDigestTx(ctx, tx)
	if err != nil {
		return err
	}
	if actual != expectedStage {
		return ErrInvalid
	}
	return nil
}

func stagedDigestTx(ctx context.Context, tx *sql.Tx) (string, error) {
	graph, err := configexport.DigestTx(ctx, tx)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = fmt.Fprintln(hash, graph)
	for _, facts := range []func(context.Context, *sql.Tx) (string, error){servers.ImportedStageFactsTx, authorization.ImportedStageFactsTx} {
		value, err := facts(ctx, tx)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%d:%s", len(value), value)
	}
	if err := httpcredentials.ValidateImportedUnsetTx(ctx, tx); err != nil {
		return "", err
	}
	if err := gitcredentials.ValidateImportedUnsetTx(ctx, tx); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
