package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

func (handler *Handler) activeCatalogCollection(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, cursor, problem := parseActiveCatalogQuery(request.URL.Query())
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	page, err := handler.activeCatalog.List(cursor, limit)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	items := make([]contract.CatalogToolDescriptor, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, contract.CatalogToolDescriptor{
			ToolDescriptor:     item.Resource,
			ServerDisplayName:  page.ServerDisplayNames[item.Resource.ServerID],
			ServerCatalogState: page.ServerStates[item.Resource.ServerID],
		})
	}
	var next *string
	if page.Next != nil {
		value := encodeActiveCatalogCursor(*page.Next)
		next = &value
	}
	writeJSON(writer, http.StatusOK, contract.CatalogPage{Catalog: page.Summary, Items: items, NextCursor: next})
}

func parseActiveCatalogQuery(query url.Values) (int, *catalog.ActiveCursor, contract.ProblemCode) {
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 {
			return 0, nil, contract.ProblemMalformedRequest
		}
	}
	limit := contract.S2ListPageDefault
	if text := query.Get("limit"); text != "" {
		value, err := strconv.Atoi(text)
		if err != nil || value < 1 || value > limitValue("s2_list_page") {
			return 0, nil, contract.ProblemMalformedRequest
		}
		limit = value
	}
	var cursor *catalog.ActiveCursor
	if text := query.Get("cursor"); text != "" {
		if len(text) > limitValue("cursor_bytes") {
			return 0, nil, contract.ProblemInvalidCursor
		}
		contents, err := base64.RawURLEncoding.DecodeString(text)
		var decoded catalog.ActiveCursor
		if err != nil || strictjson.Decode(contents, &decoded, strictjson.Options{MaxBytes: int64(limitValue("cursor_bytes")), MaxDepth: 8, RejectUnknownMembers: true}) != nil {
			return 0, nil, contract.ProblemInvalidCursor
		}
		cursor = &decoded
	}
	return limit, cursor, ""
}

func encodeActiveCatalogCursor(cursor catalog.ActiveCursor) string {
	contents, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(contents)
}
