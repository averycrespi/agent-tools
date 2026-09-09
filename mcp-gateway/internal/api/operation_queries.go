package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type operationQueryService interface {
	ActiveOperations(context.Context, string) ([]servers.Operation, bool, error)
	QueryOperations(context.Context, string, servers.OperationQuery, *servers.OperationQueryCursor, int) (servers.OperationQueryPage, error)
}

func (handler *Handler) operationQuery(writer http.ResponseWriter, request *http.Request, serverID string) bool {
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return true
	}
	enabled := false
	for _, key := range []string{"projection", "action", "status", "sort", "direction"} {
		if values.Has(key) {
			enabled = true
		}
	}
	if !enabled {
		return false
	}
	for key, members := range values {
		if len(members) != 1 || members[0] == "" {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return true
		}
		switch key {
		case "projection", "action", "status", "sort", "direction", "limit", "cursor":
		default:
			writeProblem(writer, contract.ProblemMalformedRequest)
			return true
		}
	}
	service, ok := handler.servers.(operationQueryService)
	if !ok {
		writeProblem(writer, contract.ProblemStorageUnavailable)
		return true
	}
	if values.Has("projection") {
		if values.Get("projection") != "active" || len(values) != 1 {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return true
		}
		items, more, err := service.ActiveOperations(request.Context(), serverID)
		if err != nil {
			writeServerError(writer, err)
			return true
		}
		resources := make([]contract.ServerOperation, 0, len(items))
		for i := range items {
			resources = append(resources, *operationResource(&items[i]))
		}
		writeJSON(writer, http.StatusOK, contract.ActiveServerOperations{Items: resources, HasMore: more})
		return true
	}
	query := servers.OperationQuery{Action: values.Get("action"), Status: values.Get("status"), Sort: values.Get("sort"), Direction: values.Get("direction")}
	if !query.Validate() {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return true
	}
	limit := contract.S2ListPageDefault
	if text := values.Get("limit"); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 || parsed > contract.S2ListPageDefault || strconv.Itoa(parsed) != text {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return true
		}
		limit = parsed
	}
	var cursor *servers.OperationQueryCursor
	binding := inventoryDigest(query)
	if text := values.Get("cursor"); text != "" {
		contents, err := base64.RawURLEncoding.DecodeString(text)
		var decoded servers.OperationQueryCursor
		if len(text) > limitValue("cursor_bytes") || err != nil || strictjson.Decode(contents, &decoded, strictjson.Options{MaxBytes: int64(limitValue("cursor_bytes")), MaxDepth: 8, RejectUnknownMembers: true}) != nil {
			writeProblem(writer, contract.ProblemInvalidCursor)
			return true
		}
		if decoded.Epoch != handler.inventoryEpoch || decoded.Query != binding {
			writeProblem(writer, contract.ProblemStaleCursor)
			return true
		}
		cursor = &decoded
	}
	page, err := service.QueryOperations(request.Context(), serverID, query, cursor, limit)
	if err != nil {
		writeServerError(writer, err)
		return true
	}
	items := make([]contract.ServerOperation, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, *operationResource(&page.Items[i]))
	}
	var next *string
	if page.Next != nil {
		page.Next.Epoch, page.Next.Query = handler.inventoryEpoch, binding
		contents, _ := json.Marshal(page.Next)
		value := base64.RawURLEncoding.EncodeToString(contents)
		next = &value
	}
	writeJSON(writer, http.StatusOK, contract.QueryCollection[contract.ServerOperation]{Collection: contract.Collection[contract.ServerOperation]{Items: items, NextCursor: next}, CollectionRange: page.CollectionRange})
	return true
}
