package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"golang.org/x/text/unicode/norm"
)

type inventoryQuery struct{ Name, Namespace, Status, Sort, Direction string }
type inventoryCursor struct {
	Epoch    string `json:"epoch"`
	Query    string `json:"query"`
	Digest   string `json:"digest"`
	Upper    int64  `json:"upper"`
	Position int    `json:"position"`
}
type inventoryKey struct {
	ID, Name, Namespace, Status, Revision string
	Tools                                 int64
}

func inventoryRecognition(value string) string { return strings.ToLower(norm.NFKC.String(value)) }
func inventoryDigest(value any) string {
	contents, _ := json.Marshal(value)
	digest := sha256.Sum256(contents)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
func parseInventoryQuery(raw string) (inventoryQuery, url.Values, bool, contract.ProblemCode) {
	query := inventoryQuery{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, nil, false, contract.ProblemMalformedRequest
	}
	enabled := false
	for key, destination := range map[string]*string{"name": &query.Name, "namespace": &query.Namespace, "status": &query.Status, "sort": &query.Sort, "direction": &query.Direction} {
		members, exists := values[key]
		if !exists {
			continue
		}
		enabled = true
		if len(members) != 1 || members[0] == "" || members[0] == "null" {
			return query, nil, true, contract.ProblemMalformedRequest
		}
		*destination = members[0]
		values.Del(key)
	}
	for _, value := range []string{query.Name, query.Namespace} {
		if !utf8.ValidString(value) || len(value) > 256 {
			return query, nil, enabled, contract.ProblemMalformedRequest
		}
		for _, form := range []string{value, inventoryRecognition(value)} {
			if len(form) > 256 || strings.IndexFunc(form, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
				return query, nil, enabled, contract.ProblemMalformedRequest
			}
		}
	}
	if !slices.Contains([]string{"", "Ready", "Connecting", "Authorization required", "Authentication unavailable", "Capacity saturated", "Disabled", "Deleted", "Needs attention"}, query.Status) || !slices.Contains([]string{"", "name", "id", "namespace", "status", "tools"}, query.Sort) || query.Direction != "" && (query.Sort == "" || !slices.Contains([]string{"ascending", "descending"}, query.Direction)) {
		return query, nil, enabled, contract.ProblemMalformedRequest
	}
	if enabled {
		for key, members := range values {
			if (key != "cursor" && key != "limit") || len(members) != 1 || members[0] == "" {
				return query, nil, true, contract.ProblemMalformedRequest
			}
		}
	}
	return query, values, enabled, ""
}

func inventoryStatus(server contract.Server) string {
	if server.DesiredState == contract.DesiredServerDeleted {
		return "Deleted"
	}
	if server.DesiredState == contract.DesiredServerDisabled {
		return "Disabled"
	}
	credential := string(server.CredentialState)
	if server.Runtime.State == contract.RuntimeAuthenticationRequired || credential == "absent" || credential == "reauthentication_required" {
		return "Authorization required"
	}
	if slices.Contains([]string{"locked", "interaction_required", "unavailable", "unsupported"}, credential) {
		return "Authentication unavailable"
	}
	if slices.Contains([]string{"activating", "retry_wait"}, string(server.Runtime.State)) || slices.Contains([]string{"refreshing", "disconnecting", "cleanup_pending"}, credential) {
		return "Connecting"
	}
	saturated := server.Runtime.Reconciliation.Saturated || server.Runtime.Dispatch.Saturated || server.Catalog.Traversal.Saturated
	if server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogCurrent && (credential == "ready" || credential == "not_required") && !saturated {
		return "Ready"
	}
	if server.Runtime.State != contract.RuntimeActive || server.Catalog.ActiveState != contract.ActiveCatalogCurrent {
		return "Needs attention"
	}
	return "Capacity saturated"
}

func (handler *Handler) queryServers(writer http.ResponseWriter, request *http.Request, query inventoryQuery, values url.Values) {
	limit := contract.S2ListPageDefault
	if text := values.Get("limit"); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 || parsed > 50 || strconv.Itoa(parsed) != text {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
		limit = parsed
	}
	var cursor *inventoryCursor
	if text := values.Get("cursor"); text != "" {
		contents, err := base64.RawURLEncoding.DecodeString(text)
		var decoded inventoryCursor
		if len(text) > limitValue("cursor_bytes") || err != nil || strictjson.Decode(contents, &decoded, strictjson.Options{MaxBytes: int64(limitValue("cursor_bytes")), MaxDepth: 8, RejectUnknownMembers: true}) != nil {
			writeProblem(writer, contract.ProblemInvalidCursor)
			return
		}
		cursor = &decoded
	}
	binding := inventoryDigest(query)
	var upper *int64
	if cursor != nil {
		if cursor.Epoch != handler.inventoryEpoch || cursor.Query != binding || cursor.Upper < 0 || cursor.Position < 1 {
			writeProblem(writer, contract.ProblemStaleCursor)
			return
		}
		upper = &cursor.Upper
	}
	service, ok := handler.servers.(interface {
		Inventory(context.Context, *int64) ([]servers.Server, int64, error)
	})
	if !ok {
		writeProblem(writer, contract.ProblemStorageUnavailable)
		return
	}
	stored, watermark, err := service.Inventory(request.Context(), upper)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	resources := make(map[string]contract.Server, len(stored))
	keys := make([]inventoryKey, 0, len(stored))
	for _, item := range stored {
		resource, err := handler.serverResource(request.Context(), item)
		if err != nil {
			writeServerError(writer, err)
			return
		}
		resources[item.ID] = resource
		keys = append(keys, inventoryKey{ID: item.ID, Name: item.DisplayName, Namespace: item.Namespace, Status: inventoryStatus(resource), Revision: item.DesiredRevision, Tools: resource.Catalog.ActiveToolCount})
	}
	// Bind traversal to every observed ordering/filter fact, not merely SQL revisions.
	digest := inventoryDigest(keys)
	if cursor != nil && cursor.Digest != digest {
		writeProblem(writer, contract.ProblemStaleCursor)
		return
	}
	name, namespace := inventoryRecognition(query.Name), inventoryRecognition(query.Namespace)
	keys = slices.DeleteFunc(keys, func(key inventoryKey) bool {
		return (!strings.Contains(inventoryRecognition(key.Name), name) && !strings.Contains(inventoryRecognition(key.ID), name)) || !strings.Contains(inventoryRecognition(key.Namespace), namespace) || query.Status != "" && query.Status != key.Status
	})
	slices.SortFunc(keys, func(left, right inventoryKey) int {
		leftKey, rightKey := left.Name, right.Name
		switch query.Sort {
		case "id":
			leftKey, rightKey = left.ID, right.ID
		case "namespace":
			leftKey, rightKey = left.Namespace, right.Namespace
		case "status":
			leftKey, rightKey = left.Status, right.Status
		}
		order := strings.Compare(inventoryRecognition(leftKey), inventoryRecognition(rightKey))
		if query.Sort == "tools" {
			order = 0
			if left.Tools < right.Tools {
				order = -1
			}
			if left.Tools > right.Tools {
				order = 1
			}
		}
		if order == 0 {
			return strings.Compare(left.ID, right.ID)
		}
		if query.Direction == "descending" {
			return -order
		}
		return order
	})
	position := 0
	if cursor != nil {
		position = cursor.Position
	}
	if position > len(keys) {
		writeProblem(writer, contract.ProblemStaleCursor)
		return
	}
	end := min(position+limit, len(keys))
	items := make([]contract.Server, 0, end-position)
	for _, key := range keys[position:end] {
		items = append(items, resources[key.ID])
	}
	var next *string
	if end < len(keys) {
		contents, _ := json.Marshal(inventoryCursor{Epoch: handler.inventoryEpoch, Query: binding, Digest: digest, Upper: watermark, Position: end})
		value := base64.RawURLEncoding.EncodeToString(contents)
		next = &value
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.Server]{Items: items, NextCursor: next})
}
