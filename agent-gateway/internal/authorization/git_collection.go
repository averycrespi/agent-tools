package authorization

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

var ErrInvalidGitCursor = errors.New("invalid Git collection cursor")

// GitCollectionQuery selects recognition metadata, never policy or material authority.
type GitCollectionQuery struct{ Name, Identity, Destination, Credential, Origin, Status, Principal, Repository, State, Sort, Direction string }

func (q GitCollectionQuery) Validate(kind string) bool {
	for _, v := range []string{q.Name, q.Identity, q.Destination, q.Credential, q.Origin, q.Principal, q.Repository} {
		if !utf8.ValidString(v) || len(v) > 256 || strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
			return false
		}
	}
	if q.Direction != "" && (q.Sort == "" || !slices.Contains([]string{"ascending", "descending"}, q.Direction)) {
		return false
	}
	switch kind {
	case "git_repositories":
		return q.Identity == "" && q.Origin == "" && q.Status == "" && q.Principal == "" && q.Repository == "" && q.State == "" && slices.Contains([]string{"", "id", "name", "destination", "credential"}, q.Sort)
	case "git_grants":
		return q.Name == "" && q.Destination == "" && q.Credential == "" && q.Origin == "" && q.Status == "" && slices.Contains([]string{"", "active", "expired"}, q.State) && slices.Contains([]string{"", "id", "description", "principal", "repository", "state"}, q.Sort)
	case "git_credentials":
		return q.Identity == "" && q.Destination == "" && q.Credential == "" && q.Principal == "" && q.Repository == "" && q.State == "" && slices.Contains([]string{"", "configured", "unavailable"}, q.Status) && slices.Contains([]string{"", "id", "name", "origin", "status"}, q.Sort)
	default:
		return false
	}
}

// GitCollectionCandidate contains only bounded, nonsecret snapshot facts. The
// credential owner supplies its own SQL metadata to the existing cursor owner.
type GitCollectionCandidate struct {
	ID, Name, Destination, CredentialID, CredentialName, PrincipalID, PrincipalName, RepositoryID, RepositoryName, State, Revision, Evidence string
}
type GitCollectionSelection struct {
	contract.CollectionRange
	Items      []GitCollectionCandidate
	NextCursor *string
}

func (r *Repository) SelectGitCollection(kind string, q GitCollectionQuery, items []GitCollectionCandidate, cursor string, limit int) (GitCollectionSelection, error) {
	var page GitCollectionSelection
	maximum := contract.GitRepositories
	switch kind {
	case "git_grants":
		maximum = contract.GitGrants
	case "git_credentials":
		maximum = contract.GitCredentials
	}
	if !q.Validate(kind) || limit < 1 || limit > 100 || len(items) > maximum {
		return page, ErrInvalidInput
	}
	if q.Sort == "" {
		q.Sort = "id"
	}
	if q.Direction == "" {
		q.Direction = "ascending"
	}
	contents, err := json.Marshal(struct {
		Kind  string
		Query GitCollectionQuery
		Items []GitCollectionCandidate
	}{kind, q, items})
	if err != nil {
		return page, err
	}
	digest := sha256.Sum256(contents)
	binding := base64.RawURLEncoding.EncodeToString(digest[:])
	position := SnapshotCursor{Collection: kind, Query: binding, Expires: r.clock.Now().Add(contract.AuthorizationCursorLifetime).Unix()}
	if cursor != "" {
		if len(cursor) > 2048 {
			return page, ErrInvalidGitCursor
		}
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil {
			return page, ErrInvalidGitCursor
		}
		legacy := strings.Split(string(raw), "\x00")
		if len(legacy) == 3 && legacy[0] == "v1" && legacy[1] == kind && validOpaqueID(legacy[2]) && q == (GitCollectionQuery{Sort: "id", Direction: "ascending"}) {
			position.AfterID = legacy[2]
		} else {
			if json.Unmarshal(raw, &position) != nil {
				return page, ErrInvalidGitCursor
			}
			canonical, _ := json.Marshal(position)
			if string(canonical) != string(raw) {
				return page, ErrInvalidGitCursor
			}
			if !r.authenticCursor(position) || position.Collection != kind || position.Query != binding {
				return page, ErrStaleCursor
			}
		}
	}
	matched := make([]GitCollectionCandidate, 0, len(items))
	for _, item := range items {
		if searchIdentity(item.Name, item.ID, q.Name) && searchIdentity(item.Name, item.ID, q.Identity) &&
			strings.Contains(strings.ToLower(item.Destination), strings.ToLower(strings.TrimSpace(q.Destination))) &&
			strings.Contains(strings.ToLower(item.Destination), strings.ToLower(strings.TrimSpace(q.Origin))) &&
			searchIdentity(item.CredentialName, item.CredentialID, q.Credential) && searchIdentity(item.PrincipalName, item.PrincipalID, q.Principal) && searchIdentity(item.RepositoryName, item.RepositoryID, q.Repository) &&
			(q.State == "" || q.State == item.State) && (q.Status == "" || q.Status == item.State) {
			matched = append(matched, item)
		}
	}
	slices.SortFunc(matched, func(a, b GitCollectionCandidate) int {
		order := strings.Compare(gitCollectionSort(a, q.Sort), gitCollectionSort(b, q.Sort))
		if q.Direction == "descending" {
			order = -order
		}
		if order == 0 {
			return strings.Compare(a.ID, b.ID)
		}
		return order
	})
	start := 0
	if position.AfterID != "" {
		found := false
		for i, item := range matched {
			if item.ID == position.AfterID {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return page, ErrStaleCursor
		}
	}
	end := min(start+limit, len(matched))
	if end < len(matched) {
		position.AfterID = matched[end-1].ID
		r.sealCursor(&position)
		raw, e := json.Marshal(position)
		if e != nil {
			return page, e
		}
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
	}
	page.Items = matched[start:end]
	page.CollectionRange = contract.CollectionRange{Offset: start, TotalCount: len(matched)}
	return page, nil
}
func gitCollectionSort(item GitCollectionCandidate, key string) string {
	switch key {
	case "id":
		return item.ID
	case "name":
		return normalizeSearch(item.Name)
	case "description":
		if strings.TrimSpace(item.Name) == "" {
			return normalizeSearch("Unnamed Git grant")
		}
		return normalizeSearch(item.Name)
	case "destination", "origin":
		return strings.ToLower(item.Destination)
	case "credential":
		return normalizeSearch(item.CredentialName)
	case "principal":
		return normalizeSearch(item.PrincipalName)
	case "repository":
		return normalizeSearch(item.RepositoryName)
	default:
		return item.State
	}
}

func (r *Repository) QueryGitRepositories(ctx context.Context, q GitCollectionQuery, cursor string, limit int) (contract.QueryCollection[contract.GitRepository], error) {
	var page contract.QueryCollection[contract.GitRepository]
	err := r.view(ctx, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT r.id,r.name,r.url,coalesce(r.credential_id,''),coalesce(c.name,''),r.revision,r.aliases_json FROM git_repositories r LEFT JOIN git_credentials c ON c.id=r.credential_id WHERE r.deleted=0 ORDER BY r.id LIMIT ?`, contract.GitRepositories+1)
		if e != nil {
			return e
		}
		items := []GitCollectionCandidate{}
		for rows.Next() {
			var item GitCollectionCandidate
			if e := rows.Scan(&item.ID, &item.Name, &item.Destination, &item.CredentialID, &item.CredentialName, &item.Revision, &item.Evidence); e != nil {
				_ = rows.Close()
				return e
			}
			items = append(items, item)
		}
		if e := errors.Join(rows.Err(), rows.Close()); e != nil {
			return e
		}
		selected, e := r.SelectGitCollection("git_repositories", q, items, cursor, limit)
		if e != nil {
			return e
		}
		page.Items = make([]contract.GitRepository, 0, len(selected.Items))
		for _, item := range selected.Items {
			rec, e := gitRepositoryTx(ctx, tx, item.ID)
			if e != nil {
				return e
			}
			page.Items = append(page.Items, rec.resource)
		}
		page.NextCursor = selected.NextCursor
		page.CollectionRange = selected.CollectionRange
		return nil
	})
	return page, err
}
func (r *Repository) QueryGitGrants(ctx context.Context, q GitCollectionQuery, cursor string, limit int) (contract.QueryCollection[contract.GitGrant], error) {
	var page contract.QueryCollection[contract.GitGrant]
	err := r.view(ctx, func(tx *sql.Tx) error {
		now := r.clock.Now()
		rows, e := tx.QueryContext(ctx, `SELECT g.id,coalesce(g.description,''),g.principal_id,p.display_name,g.repository_id,r.name,g.revision,g.expires_at FROM git_grants g JOIN principals p ON p.id=g.principal_id JOIN git_repositories r ON r.id=g.repository_id ORDER BY g.id LIMIT ?`, contract.GitGrants+1)
		if e != nil {
			return e
		}
		items := []GitCollectionCandidate{}
		for rows.Next() {
			var item GitCollectionCandidate
			var expiry sql.NullString
			if e := rows.Scan(&item.ID, &item.Name, &item.PrincipalID, &item.PrincipalName, &item.RepositoryID, &item.RepositoryName, &item.Revision, &expiry); e != nil {
				_ = rows.Close()
				return e
			}
			item.State = "active"
			if expiry.Valid {
				at, e := time.Parse(time.RFC3339Nano, expiry.String)
				if e != nil {
					_ = rows.Close()
					return e
				}
				item.Evidence = expiry.String
				if !at.After(now) {
					item.State = "expired"
				}
			}
			items = append(items, item)
		}
		if e := errors.Join(rows.Err(), rows.Close()); e != nil {
			return e
		}
		selected, e := r.SelectGitCollection("git_grants", q, items, cursor, limit)
		if e != nil {
			return e
		}
		page.Items = make([]contract.GitGrant, 0, len(selected.Items))
		for _, item := range selected.Items {
			rec, e := gitGrantTx(ctx, tx, item.ID, now)
			if e != nil {
				return e
			}
			page.Items = append(page.Items, rec)
		}
		page.NextCursor = selected.NextCursor
		page.CollectionRange = selected.CollectionRange
		return nil
	})
	return page, err
}
