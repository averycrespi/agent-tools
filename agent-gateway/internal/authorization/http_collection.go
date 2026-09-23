package authorization

import (
	"context"
	"database/sql"
	"slices"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type HTTPGrantPage struct {
	contract.CollectionRange
	Items []contract.HTTPGrantTableItem
	Next  *SnapshotCursor
}

func (r *Repository) QueryHTTPGrants(ctx context.Context, query CollectionQuery, cursor *SnapshotCursor, limit int) (page HTTPGrantPage, err error) {
	kind := query.Effect
	query.Effect = ""
	if !query.Validate(grantCollection) || query.ServerID != "" || query.Representation != "" || validatePageLimit(limit) != nil || kind != "" && !slices.Contains(contract.HTTPGrantTypes(), contract.HTTPGrantType(kind)) {
		return page, ErrInvalidInput
	}
	query.Effect = kind
	query.Representation = "http"
	if query.Sort == "" {
		query.Sort = "description"
	}
	err = r.view(ctx, func(tx *sql.Tx) error {
		// SQL projects only compact recognition facts. Full policies are loaded for
		// the selected page, not retained in the cursor/sort snapshot.
		rows, err := tx.QueryContext(ctx, `SELECT g.id,coalesce(g.description,''),g.principal_id,p.display_name,g.revision,g.expires_at,g.insertion_sequence,json_extract(g.policy_json,'$.type'),coalesce(json_extract(g.policy_json,'$.destination.host'),json_extract(g.policy_json,'$.request.origin.host')) FROM http_grants g JOIN principals p ON p.id=g.principal_id ORDER BY g.id LIMIT ?`, contract.HTTPPolicyGrants+1)
		if err != nil {
			return err
		}
		candidates := []collectionCandidate{}
		now := r.clock.Now()
		for rows.Next() {
			var item collectionCandidate
			var expiry sql.NullString
			if err := rows.Scan(&item.ID, &item.Name, &item.PrincipalID, &item.PrincipalName, &item.Revision, &expiry, &item.Sequence, &item.Effect, &item.ServerName); err != nil {
				_ = rows.Close()
				return err
			}
			item.State = string(contract.GrantActive)
			if expiry.Valid {
				parsed, ok := canonicalTimestamp(expiry.String)
				if !ok {
					_ = rows.Close()
					return ErrInvalidState
				}
				if !parsed.After(now) {
					item.State = string(contract.GrantExpired)
				}
			}
			candidates = append(candidates, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		service := CollectionService{repository: r}
		selected, err := service.selectPage(grantCollection, query, candidates, cursor, limit)
		if err != nil {
			return err
		}
		page.Items = []contract.HTTPGrantTableItem{}
		for _, candidate := range selected.items {
			g, _, err := httpGrantTx(ctx, tx, candidate.ID, now)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, contract.HTTPGrantTableItem{Grant: g, PrincipalDisplayName: candidate.PrincipalName})
		}
		page.CollectionRange = selected.CollectionRange
		page.Next = selected.next
		return nil
	})
	return
}
