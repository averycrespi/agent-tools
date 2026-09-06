package authorization

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"golang.org/x/text/unicode/norm"
)

type CollectionQuery struct {
	Name           string
	Identity       string
	Principal      string
	Target         string
	State          string
	Visibility     string
	Effect         string
	PrincipalID    string
	ServerID       string
	Sort           string
	Direction      string
	Representation string
}

type GrantDisplayNames interface {
	GrantDisplayNamesTx(context.Context, *sql.Tx) (map[string]string, error)
}

type CollectionService struct {
	repository *Repository
	targets    GrantDisplayNames
}

type GrantTablePage struct {
	contract.CollectionRange
	Items []contract.GrantTableItem
	Next  *SnapshotCursor
}

type collectionSelection struct {
	contract.CollectionRange
	items []collectionCandidate
	next  *SnapshotCursor
}

func NewCollectionService(repository *Repository, targets GrantDisplayNames) (*CollectionService, error) {
	if repository == nil || targets == nil {
		return nil, errors.New("collection query dependencies are incomplete")
	}
	return &CollectionService{repository: repository, targets: targets}, nil
}

func (query CollectionQuery) Validate(collection string) bool {
	for _, value := range []string{query.Name, query.Identity, query.Principal, query.Target} {
		if !utf8.ValidString(value) || len(value) > 256 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
			return false
		}
	}
	if query.Direction != "" && (query.Sort == "" || query.Direction != "ascending" && query.Direction != "descending") {
		return false
	}
	if query.PrincipalID != "" && !validOpaqueID(query.PrincipalID) || query.ServerID != "" && !validOpaqueID(query.ServerID) {
		return false
	}
	if collection == principalCollection {
		return query.Representation == "" && query.Identity == "" && query.Principal == "" && query.Target == "" && query.Effect == "" && query.PrincipalID == "" && query.ServerID == "" &&
			(query.State == "" || validPrincipalState(contract.PrincipalState(query.State))) &&
			(query.Visibility == "" || validVisibility(contract.PrincipalVisibility(query.Visibility))) &&
			slices.Contains([]string{"", "name", "id", "state", "visibility"}, query.Sort)
	}
	return collection == grantCollection && (query.Representation == "" || query.Representation == "table") && query.Name == "" && query.Visibility == "" &&
		slices.Contains([]string{"", "active", "expired"}, query.State) && slices.Contains([]string{"", "allow", "deny"}, query.Effect) &&
		slices.Contains([]string{"", "id", "description", "principal", "target", "effect", "state"}, query.Sort)
}

type collectionCandidate struct {
	ID            string
	Name          string
	PrincipalID   string
	PrincipalName string
	ServerID      string
	ServerName    string
	UpstreamName  string
	State         string
	Visibility    string
	Effect        string
	Revision      string
	Sequence      int64
}

func (service *CollectionService) QueryPrincipals(ctx context.Context, query CollectionQuery, cursor *SnapshotCursor, limit int) (PrincipalPage, error) {
	if !query.Validate(principalCollection) || validatePageLimit(limit) != nil {
		return PrincipalPage{}, ErrInvalidInput
	}
	var page PrincipalPage
	err := service.repository.view(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, display_name, state, visibility, revision, insertion_sequence FROM principals ORDER BY insertion_sequence LIMIT ?`, mustLimit("principals")+1)
		if err != nil {
			return err
		}
		candidates := make([]collectionCandidate, 0)
		for rows.Next() {
			var item collectionCandidate
			if err := rows.Scan(&item.ID, &item.Name, &item.State, &item.Visibility, &item.Revision, &item.Sequence); err != nil {
				_ = rows.Close()
				return err
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
		selected, err := service.selectPage(principalCollection, query, candidates, cursor, limit)
		if err != nil {
			return err
		}
		page.Items = make([]contract.Principal, 0, len(selected.items))
		for _, candidate := range selected.items {
			item, err := principalByIDTx(ctx, tx, candidate.ID)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		page.Next = selected.next
		page.CollectionRange = selected.CollectionRange
		return nil
	})
	return page, err
}

func (service *CollectionService) QueryGrants(ctx context.Context, query CollectionQuery, cursor *SnapshotCursor, limit int) (GrantTablePage, error) {
	if !query.Validate(grantCollection) || validatePageLimit(limit) != nil {
		return GrantTablePage{}, ErrInvalidInput
	}
	var page GrantTablePage
	err := service.repository.view(ctx, func(tx *sql.Tx) error {
		names, err := service.targets.GrantDisplayNamesTx(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT g.id, coalesce(g.description, ''), g.principal_id, p.display_name,
			g.server_id, coalesce(g.upstream_name, 'Entire server'), g.effect, g.expires_at, g.revision, g.insertion_sequence
			FROM grants g JOIN principals p ON p.id = g.principal_id ORDER BY g.insertion_sequence LIMIT ?`, mustLimit("grants")+1)
		if err != nil {
			return err
		}
		now := service.repository.clock.Now()
		candidates := make([]collectionCandidate, 0)
		for rows.Next() {
			var item collectionCandidate
			var expiry sql.NullString
			if err := rows.Scan(&item.ID, &item.Name, &item.PrincipalID, &item.PrincipalName, &item.ServerID, &item.UpstreamName, &item.Effect, &expiry, &item.Revision, &item.Sequence); err != nil {
				_ = rows.Close()
				return err
			}
			item.ServerName = names[item.ServerID]
			if item.ServerName == "" {
				item.ServerName = "Server " + item.ServerID
			}
			item.State = string(contract.GrantActive)
			if expiry.Valid {
				at, err := time.Parse(time.RFC3339Nano, expiry.String)
				if err != nil {
					_ = rows.Close()
					return err
				}
				if !at.After(now) {
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
		selected, err := service.selectPage(grantCollection, query, candidates, cursor, limit)
		if err != nil {
			return err
		}
		page.Items = make([]contract.GrantTableItem, 0, len(selected.items))
		for _, candidate := range selected.items {
			_, grant, err := service.repository.scanGrant(tx.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, candidate.ID), now)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, contract.GrantTableItem{Grant: grant, PrincipalDisplayName: candidate.PrincipalName, ServerDisplayName: candidate.ServerName})
		}
		page.Next = selected.next
		page.CollectionRange = selected.CollectionRange
		return nil
	})
	return page, err
}

func (service *CollectionService) selectPage(collection string, query CollectionQuery, candidates []collectionCandidate, cursor *SnapshotCursor, limit int) (collectionSelection, error) {
	if int64(len(candidates)) > mustLimit(collection) {
		return collectionSelection{}, ErrResourceLimit
	}
	// Only compact recognition metadata is scanned; policy bodies and credentials are read for the selected page.
	contents, err := json.Marshal(struct {
		Collection string
		Query      CollectionQuery
		Candidates []collectionCandidate
	}{collection, query, candidates})
	if err != nil {
		return collectionSelection{}, err
	}
	digest := sha256.Sum256(contents)
	binding := base64.RawURLEncoding.EncodeToString(digest[:])
	position := SnapshotCursor{Collection: collection, Query: binding, Expires: service.repository.clock.Now().Add(contract.AuthorizationCursorLifetime).Unix()}
	if cursor != nil {
		position = *cursor
		if !service.repository.authenticCursor(position) || position.Collection != collection || position.Query != binding || position.After < 0 || position.After > int64(len(candidates)) {
			return collectionSelection{}, ErrStaleCursor
		}
	}
	filtered := make([]collectionCandidate, 0, len(candidates))
	for _, item := range candidates {
		if candidateMatches(item, query) {
			filtered = append(filtered, item)
		}
	}
	slices.SortFunc(filtered, func(a, b collectionCandidate) int {
		var order int
		if query.Sort == "" {
			if a.Sequence < b.Sequence {
				order = -1
			} else if a.Sequence > b.Sequence {
				order = 1
			}
		} else {
			order = strings.Compare(candidateSortValue(a, query.Sort), candidateSortValue(b, query.Sort))
		}
		if query.Direction == "descending" {
			order = -order
		}
		if order == 0 {
			return strings.Compare(a.ID, b.ID)
		}
		return order
	})
	start := int(position.After)
	if start > len(filtered) || start > 0 && filtered[start-1].ID != position.AfterID {
		return collectionSelection{}, ErrStaleCursor
	}
	end := min(start+limit, len(filtered))
	var next *SnapshotCursor
	if end < len(filtered) {
		position.After = int64(end)
		position.AfterID = filtered[end-1].ID
		position.Upper = int64(len(candidates))
		service.repository.sealCursor(&position)
		next = &position
	}
	return collectionSelection{
		CollectionRange: contract.CollectionRange{TotalCount: len(filtered), Offset: start},
		items:           filtered[start:end], next: next,
	}, nil
}

func candidateMatches(item collectionCandidate, query CollectionQuery) bool {
	return searchIdentity(item.Name, item.ID, query.Name) && searchIdentity(item.Name, item.ID, query.Identity) &&
		searchIdentity(item.PrincipalName, item.PrincipalID, query.Principal) && searchIdentity(item.ServerName+" "+item.UpstreamName, item.ServerID, query.Target) &&
		(query.State == "" || query.State == item.State) && (query.Visibility == "" || query.Visibility == item.Visibility) &&
		(query.Effect == "" || query.Effect == item.Effect) && (query.PrincipalID == "" || query.PrincipalID == item.PrincipalID) && (query.ServerID == "" || query.ServerID == item.ServerID)
}

func candidateSortValue(item collectionCandidate, key string) string {
	switch key {
	case "name", "description":
		return normalizeSearch(item.Name)
	case "id":
		return item.ID
	case "principal":
		return normalizeSearch(item.PrincipalName)
	case "target":
		return normalizeSearch(item.ServerName)
	case "state":
		return item.State
	case "visibility":
		return item.Visibility
	case "effect":
		return item.Effect
	default:
		panic(fmt.Sprintf("unvalidated collection sort %q", key))
	}
}

func normalizeSearch(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsMark(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFKD.String(value))
}

func searchIdentity(value, id, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" || strings.Contains(id, query) {
		return true
	}
	candidate := normalizeSearch(value)
	for _, token := range strings.Fields(normalizeSearch(query)) {
		if strings.Contains(candidate, token) {
			continue
		}
		if len([]rune(token)) < 4 || strings.IndexFunc(token, unicode.IsDigit) >= 0 {
			return false
		}
		words := strings.FieldsFunc(candidate, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
		if !slices.ContainsFunc(words, func(word string) bool { return withinOneEdit([]rune(word), []rune(token)) }) {
			return false
		}
	}
	return true
}

func withinOneEdit(a, b []rune) bool {
	if len(a)-len(b) > 1 || len(b)-len(a) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		switch {
		case len(a) == len(b) && i+1 < len(a) && j+1 < len(b) && a[i] == b[j+1] && a[i+1] == b[j]:
			i += 2
			j += 2
		case len(a) > len(b):
			i++
		case len(b) > len(a):
			j++
		default:
			i++
			j++
		}
	}
	return edits+len(a)-i+len(b)-j <= 1
}
