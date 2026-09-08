package grantrequests

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type AdminQuery struct {
	Request   string
	Principal string
	Target    string
	Scope     string
	Sort      string
	Direction string
}

func (query AdminQuery) Valid() bool {
	for _, value := range []string{query.Request, query.Principal, query.Target} {
		if !utf8.ValidString(value) || len(value) > 256 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
			return false
		}
	}
	return slices.Contains([]string{"", "tool", "server"}, query.Scope) &&
		slices.Contains([]string{"", "request", "principal", "target", "state", "submitted"}, query.Sort) &&
		(query.Direction == "" || query.Sort != "" && slices.Contains([]string{"ascending", "descending"}, query.Direction))
}

type requestDisplayNames interface {
	GrantDisplayNamesTx(context.Context, *sql.Tx) (map[string]string, error)
}

type requestCandidate struct {
	ID            string
	PrincipalID   string
	PrincipalName string
	ServerID      string
	ServerName    string
	Upstream      *string
	Scope         string
	Target        string
	State         string
	Revision      string
	CreatedAt     string
	Sequence      int64
}

func (repository *Repository) queryAdmin(ctx context.Context, filter AdminFilter, cursor *AdminCursor, limit int) (AdminPage, error) {
	if !filter.Query.Valid() {
		return AdminPage{}, ErrInvalidInput
	}
	targets, ok := repository.namespaces.(requestDisplayNames)
	if !ok || repository.principalNames == nil {
		return AdminPage{}, ErrIdentityUnavailable
	}
	key, err := repository.adminQueryKey()
	if err != nil {
		return AdminPage{}, err
	}
	var page AdminPage
	err = repository.view(ctx, func(tx *sql.Tx) error {
		names, err := targets.GrantDisplayNamesTx(ctx, tx)
		if err != nil {
			return err
		}
		principals, err := repository.principalNames.PrincipalDisplayNamesTx(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT r.id, r.principal_id, r.resolved_server_id,
  r.resolved_upstream_name, r.requested_scope, r.requested_target, r.state, r.revision, r.created_at, r.insertion_sequence
  FROM grant_requests r ORDER BY r.insertion_sequence LIMIT ?`, fixedLimit("grant_requests")+1)
		if err != nil {
			return err
		}
		candidates := make([]requestCandidate, 0)
		for rows.Next() {
			var item requestCandidate
			if err := rows.Scan(&item.ID, &item.PrincipalID, &item.ServerID, &item.Upstream, &item.Scope, &item.Target, &item.State, &item.Revision, &item.CreatedAt, &item.Sequence); err != nil {
				_ = rows.Close()
				return err
			}
			item.PrincipalName = principals[item.PrincipalID]
			if item.PrincipalName == "" {
				item.PrincipalName = "Principal " + item.PrincipalID
			}
			item.ServerName = names[item.ServerID]
			if item.ServerName == "" {
				item.ServerName = "Server " + item.ServerID
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
		if int64(len(candidates)) > fixedLimit("grant_requests") {
			return ErrResourceLimit
		}
		contents, err := json.Marshal(struct {
			Filter     AdminFilter
			Candidates []requestCandidate
		}{filter, candidates})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(contents)
		binding := base64.RawURLEncoding.EncodeToString(digest[:])
		now := repository.clock.Now()
		position := AdminCursor{Collection: adminRequestCollection, Query: binding, Expires: now.Add(contract.AuthorizationCursorLifetime).Unix()}
		if cursor != nil {
			position = *cursor
			mac, err := base64.RawURLEncoding.DecodeString(position.MAC)
			if err != nil || !hmac.Equal(mac, adminQueryMAC(key, position)) || position.Collection != adminRequestCollection || position.Query != binding || position.Expires <= now.Unix() || position.After < 0 || position.After > int64(len(candidates)) {
				return ErrStaleCursor
			}
		}
		selected := make([]requestCandidate, 0, len(candidates))
		for _, item := range candidates {
			if item.matches(filter) {
				selected = append(selected, item)
			}
		}
		slices.SortFunc(selected, func(a, b requestCandidate) int {
			order := strings.Compare(a.sortValue(filter.Query.Sort), b.sortValue(filter.Query.Sort))
			if filter.Query.Sort == "" || filter.Query.Sort == "submitted" {
				order = 0
				if a.Sequence < b.Sequence {
					order = -1
				} else if a.Sequence > b.Sequence {
					order = 1
				}
			}
			if filter.Query.Direction == "descending" {
				order = -order
			}
			if order == 0 {
				return strings.Compare(a.ID, b.ID)
			}
			return order
		})
		start := int(position.After)
		if start > len(selected) || start > 0 && selected[start-1].ID != position.AfterID {
			return ErrStaleCursor
		}
		end := min(start+limit, len(selected))
		page.CollectionRange = contract.CollectionRange{TotalCount: len(selected), Offset: start}
		page.Table = make([]contract.GrantRequestTableItem, 0, end-start)
		for _, item := range selected[start:end] {
			_, request, err := scanAgentRequest(tx.QueryRowContext(ctx, agentRequestSelect+` WHERE id = ?`, item.ID))
			if err != nil {
				return err
			}
			page.Table = append(page.Table, contract.GrantRequestTableItem{Request: requestSummary(item.PrincipalID, request), PrincipalDisplayName: item.PrincipalName, ServerDisplayName: item.ServerName, ResolvedServerID: item.ServerID, ResolvedUpstreamName: item.Upstream})
		}
		if end < len(selected) {
			position.After, position.AfterID = int64(end), selected[end-1].ID
			position.MAC = base64.RawURLEncoding.EncodeToString(adminQueryMAC(key, position))
			page.Next = &position
		}
		return nil
	})
	return page, err
}

func (item requestCandidate) matches(filter AdminFilter) bool {
	query := filter.Query
	upstream := "All tools"
	if item.Upstream != nil {
		upstream = *item.Upstream
	}
	return (filter.PrincipalID == "" || filter.PrincipalID == item.PrincipalID) &&
		(filter.State == nil || string(*filter.State) == item.State) && (query.Scope == "" || query.Scope == item.Scope) &&
		strings.Contains(item.ID, strings.TrimSpace(query.Request)) &&
		requestSearch(item.PrincipalName, item.PrincipalID, query.Principal) &&
		requestSearch(item.ServerName+" "+upstream+" "+item.Target, item.ServerID, query.Target)
}

func requestSearch(name, id, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" || strings.Contains(id, query) {
		return true
	}
	name = strings.ToLower(name)
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(name, token) {
			return false
		}
	}
	return true
}

func (item requestCandidate) sortValue(key string) string {
	switch key {
	case "principal":
		return strings.ToLower(item.PrincipalName)
	case "target":
		return strings.ToLower(item.ServerName + " " + item.Target)
	case "state":
		return item.State
	default:
		return item.ID
	}
}

func (repository *Repository) adminQueryKey() ([]byte, error) {
	repository.entropyMu.Lock()
	defer repository.entropyMu.Unlock()
	if repository.queryKey == nil {
		key := make([]byte, 32)
		if _, err := io.ReadFull(repository.entropy, key); err != nil {
			return nil, err
		}
		repository.queryKey = key
	}
	return repository.queryKey, nil
}

func adminQueryMAC(key []byte, cursor AdminCursor) []byte {
	cursor.MAC = ""
	contents, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(contents)
	return mac.Sum(nil)
}
