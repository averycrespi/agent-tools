package grantrequests

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

func (repository *Repository) Occupancy(ctx context.Context) (contract.LimitStatus, contract.LimitStatus, error) {
	if repository == nil || repository.store == nil {
		return contract.LimitStatus{}, contract.LimitStatus{}, ErrStorageUnavailable
	}
	var requests, evidence int64
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM grant_requests`).Scan(&requests); err != nil {
			return err
		}
		return transaction.QueryRowContext(ctx, `SELECT total_bytes FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(&evidence)
	})
	if err != nil {
		return contract.LimitStatus{}, contract.LimitStatus{}, err
	}
	requestLimit := fixedLimit("grant_requests")
	evidenceLimit := fixedLimit("grant_request_evidence_bytes")
	if requests < 0 || requests > requestLimit || evidence < 0 || evidence > evidenceLimit {
		return contract.LimitStatus{}, contract.LimitStatus{}, ErrInvalidState
	}
	return contract.LimitStatus{InUse: requests, Limit: requestLimit, Saturated: requests >= requestLimit},
		contract.LimitStatus{InUse: evidence, Limit: evidenceLimit, Saturated: evidence >= evidenceLimit}, nil
}
