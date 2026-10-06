package gitcredentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// Query reads safe metadata for selection, then hydrates only the chosen page.
// Availability is published SQL authority, never a provider or network probe.
func (s *Service) Query(ctx context.Context, q authorization.GitCollectionQuery, cursor string, limit int) (contract.QueryCollection[contract.GitCredential], error) {
	var page contract.QueryCollection[contract.GitCredential]
	latched := s.store.Latched()
	err := s.store.View(ctx, func(tx *sql.Tx) error {
		refs, e := authorization.GitCredentialReferenceIDsTx(ctx, tx)
		if e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT c.id,c.name,c.origin,c.revision,c.material_revision,
   c.handle IS NOT NULL AND (SELECT count(*) FROM keyring_authorities a WHERE a.owner=c.id AND a.kind='git_credential' AND a.handle=c.handle AND a.revision=c.material_revision AND NOT EXISTS (SELECT 1 FROM keyring_authority_fences f WHERE f.owner=a.owner AND f.kind=a.kind))=1
   FROM git_credentials c WHERE c.deleted=0 ORDER BY c.id LIMIT ?`, contract.GitCredentials+1)
		if e != nil {
			return e
		}
		items := []authorization.GitCollectionCandidate{}
		for rows.Next() {
			var item authorization.GitCollectionCandidate
			var material string
			var available bool
			if e := rows.Scan(&item.ID, &item.Name, &item.Destination, &item.Revision, &material, &available); e != nil {
				_ = rows.Close()
				return e
			}
			item.State = "unavailable"
			if available && !latched {
				item.State = "configured"
			}
			evidence, e := json.Marshal(struct {
				Material   string
				References []contract.GitCredentialReference
			}{material, refs[item.ID]})
			if e != nil {
				_ = rows.Close()
				return e
			}
			item.Evidence = string(evidence)
			items = append(items, item)
		}
		if e := errors.Join(rows.Err(), rows.Close()); e != nil {
			return e
		}
		selected, e := s.authority.SelectGitCollection("git_credentials", q, items, cursor, limit)
		if e != nil {
			return e
		}
		page.Items = make([]contract.GitCredential, 0, len(selected.Items))
		for _, item := range selected.Items {
			rec, e := readTx(ctx, tx, item.ID)
			if e != nil {
				return e
			}
			rec.Available = item.State == "configured"
			rec.References = append(rec.References, refs[item.ID]...)
			page.Items = append(page.Items, rec.GitCredential)
		}
		page.NextCursor = selected.NextCursor
		page.CollectionRange = selected.CollectionRange
		return nil
	})
	// Do not return configured-filter matches after the storage latch changed.
	if err == nil && latched != s.store.Latched() {
		return contract.QueryCollection[contract.GitCredential]{}, ErrUnavailable
	}
	return page, err
}
