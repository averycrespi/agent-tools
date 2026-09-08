package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"golang.org/x/text/unicode/norm"
)

type ToolQuery struct {
	Tool      string
	Server    string
	Status    string
	Sort      string
	Direction string
}

func (query ToolQuery) Validate(aggregate bool) bool {
	for _, value := range []string{query.Tool, query.Server} {
		if !utf8.ValidString(value) || len(value) > 256 {
			return false
		}
		for _, form := range []string{value, toolRecognition(value)} {
			if len(form) > 256 || strings.IndexFunc(form, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
				return false
			}
		}
	}
	if query.Direction != "" && (query.Sort == "" || !slices.Contains([]string{"ascending", "descending"}, query.Direction)) {
		return false
	}
	if aggregate {
		return slices.Contains([]string{"", "available", "issue"}, query.Status) && slices.Contains([]string{"", "tool", "server"}, query.Sort)
	}
	return query.Server == "" && slices.Contains([]string{"", "available", "retired"}, query.Status) && slices.Contains([]string{"", "tool", "status", "last-seen"}, query.Sort)
}

func toolRecognition(value string) string {
	return strings.ToLower(norm.NFKC.String(value))
}

func (query ToolQuery) binding() string {
	contents, _ := json.Marshal(query)
	digest := sha256.Sum256(contents)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

type toolCandidate struct {
	id       string
	sequence int64
	name     string
	status   string
	lastSeen string
}

func (repository *Repository) QueryDescriptors(ctx context.Context, serverID string, query ToolQuery, cursor *DescriptorCursor, limit int) (DescriptorPage, error) {
	if !query.Validate(false) || limit < 1 || limit > contract.S2ListPageDefault {
		return DescriptorPage{}, servers.ErrInvalidInput
	}
	var page DescriptorPage
	err := repository.store.View(ctx, func(tx *sql.Tx) error {
		status, err := statusTx(ctx, tx, serverID)
		if err != nil {
			return err
		}
		revision := "0"
		if status.Revision != nil {
			revision = *status.Revision
		}
		upper, position := int64(0), 0
		binding := query.binding()
		if cursor != nil {
			if cursor.ServerID != serverID || cursor.CatalogRevision != revision || cursor.Query != binding || cursor.Retired != contract.DescriptorRetiredInclude || cursor.Upper < 0 || cursor.After != 0 || cursor.AfterID != "" || cursor.Position < 1 {
				return servers.ErrStaleCursor
			}
			upper, position = cursor.Upper, cursor.Position
		} else if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(insertion_sequence), 0) FROM durable_tool_identities WHERE server_id = ?`, serverID).Scan(&upper); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT identity.id, identity.insertion_sequence, identity.external_name, descriptor.last_seen_at, descriptor.retired_at IS NOT NULL
			FROM durable_tool_identities AS identity JOIN tool_descriptors AS descriptor ON descriptor.tool_id = identity.id
			WHERE identity.server_id = ? AND identity.insertion_sequence <= ? ORDER BY identity.insertion_sequence LIMIT ?`, serverID, upper, fixedLimit("durable_tool_identities_per_server")+1)
		if err != nil {
			return err
		}
		candidates := make([]toolCandidate, 0)
		for rows.Next() {
			var item toolCandidate
			var retired bool
			if err := rows.Scan(&item.id, &item.sequence, &item.name, &item.lastSeen, &retired); err != nil {
				_ = rows.Close()
				return err
			}
			item.status = "available"
			if retired {
				item.status = "retired"
			}
			candidates = append(candidates, item)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		if int64(len(candidates)) > fixedLimit("durable_tool_identities_per_server") {
			return servers.ErrStorageUnavailable
		}
		candidates = slices.DeleteFunc(candidates, func(item toolCandidate) bool {
			return !strings.Contains(toolRecognition(item.name), toolRecognition(query.Tool)) || query.Status != "" && query.Status != item.status
		})
		sortKey, direction := query.Sort, query.Direction
		if sortKey == "" {
			sortKey, direction = "last-seen", "descending"
		}
		slices.SortFunc(candidates, func(left, right toolCandidate) int {
			leftKey, rightKey := toolRecognition(left.name), toolRecognition(right.name)
			switch sortKey {
			case "status":
				leftKey, rightKey = left.status, right.status
			case "last-seen":
				leftKey, rightKey = left.lastSeen, right.lastSeen
			}
			order := strings.Compare(leftKey, rightKey)
			if order == 0 {
				return strings.Compare(left.id, right.id)
			}
			if direction == "descending" {
				return -order
			}
			return order
		})
		if position > len(candidates) {
			return servers.ErrStaleCursor
		}
		end := min(position+limit, len(candidates))
		page.Items = make([]DescriptorRecord, 0, end-position)
		for _, item := range candidates[position:end] {
			var record DescriptorRecord
			if err := scanDescriptor(tx.QueryRowContext(ctx, descriptorSelect+` WHERE identity.id = ? AND identity.server_id = ?`, item.id, serverID), &record.InsertionSequence, &record.Resource); err != nil {
				return err
			}
			page.Items = append(page.Items, record)
		}
		if end < len(candidates) {
			page.Next = &DescriptorCursor{ServerID: serverID, Retired: contract.DescriptorRetiredInclude, CatalogRevision: revision, Upper: upper, Query: binding, Position: end}
		}
		return nil
	})
	return page, mapStoreError(err)
}

func (registry *ActiveRegistry) Query(query ToolQuery, cursor *ActiveCursor, limit int) (ActivePage, error) {
	if !query.Validate(true) || limit < 1 || limit > contract.S2ListPageDefault {
		return ActivePage{}, servers.ErrInvalidInput
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	generation, binding := registry.generationLocked(), query.binding()
	position, upper := 0, int64(0)
	if cursor != nil {
		if cursor.Generation != generation || cursor.Query != binding || cursor.Position < 1 || cursor.Upper < 0 || cursor.After != 0 || cursor.AfterID != "" {
			return ActivePage{}, servers.ErrStaleCursor
		}
		position, upper = cursor.Position, cursor.Upper
	}
	page := ActivePage{Summary: registry.summaryLocked(), ServerDisplayNames: make(map[string]string), ServerStates: make(map[string]contract.ActiveCatalogState)}
	candidates := make([]DescriptorRecord, 0)
	toolFilter, serverFilter := toolRecognition(query.Tool), toolRecognition(query.Server)
	for serverID, snapshot := range registry.servers {
		if snapshot.State == contract.ActiveCatalogAbsent || snapshot.State == contract.ActiveCatalogUnavailable {
			continue
		}
		page.ServerDisplayNames[serverID], page.ServerStates[serverID] = snapshot.ServerDisplayName, snapshot.State
		status := "issue"
		if snapshot.State == contract.ActiveCatalogCurrent {
			status = "available"
		}
		for _, tool := range snapshot.Tools {
			item := tool.Record
			if cursor == nil {
				upper = max(upper, item.InsertionSequence)
			}
			if cursor != nil && item.InsertionSequence > upper || query.Status != "" && query.Status != status || !strings.Contains(toolRecognition(snapshot.ServerDisplayName), serverFilter) || !strings.Contains(toolRecognition(item.Resource.ExternalName), toolFilter) {
				continue
			}
			candidates = append(candidates, item)
		}
	}
	slices.SortFunc(candidates, func(left, right DescriptorRecord) int {
		leftKey, rightKey := left.Resource.ExternalName, right.Resource.ExternalName
		if query.Sort == "server" {
			leftKey, rightKey = page.ServerDisplayNames[left.Resource.ServerID], page.ServerDisplayNames[right.Resource.ServerID]
		}
		order := strings.Compare(toolRecognition(leftKey), toolRecognition(rightKey))
		if order == 0 {
			return strings.Compare(left.Resource.ID, right.Resource.ID)
		}
		if query.Direction == "descending" {
			return -order
		}
		return order
	})
	if position > len(candidates) {
		return ActivePage{}, servers.ErrStaleCursor
	}
	end := min(position+limit, len(candidates))
	page.Items = make([]DescriptorRecord, 0, end-position)
	for _, item := range candidates[position:end] {
		page.Items = append(page.Items, cloneDescriptorRecord(item))
	}
	if end < len(candidates) {
		page.Next = &ActiveCursor{Generation: generation, Upper: upper, Query: binding, Position: end}
	}
	return page, nil
}
