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

func (handler *Handler) descriptorsCollection(writer http.ResponseWriter, request *http.Request, serverID string) {
	if request.Method != http.MethodGet {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, retired, cursor, summary, problem := parseDescriptorQuery(request.URL.Query())
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	page, err := handler.catalog.ListDescriptors(request.Context(), serverID, retired, cursor, limit)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	var next *string
	if page.Next != nil {
		value := encodeDescriptorCursor(*page.Next)
		next = &value
	}
	if summary {
		items := make([]contract.ToolDescriptorSummary, 0, len(page.Items))
		for _, item := range page.Items {
			resource := item.Resource
			items = append(items, contract.ToolDescriptorSummary{
				ID: resource.ID, ServerID: resource.ServerID,
				UpstreamName: resource.UpstreamName, ExternalName: resource.ExternalName,
				CatalogRevision: resource.CatalogRevision,
			})
		}
		writeJSON(writer, http.StatusOK, contract.Collection[contract.ToolDescriptorSummary]{Items: items, NextCursor: next})
		return
	}
	items := make([]contract.ToolDescriptor, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, item.Resource)
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.ToolDescriptor]{Items: items, NextCursor: next})
}

func (handler *Handler) descriptorMember(writer http.ResponseWriter, request *http.Request, serverID, toolID string) {
	if request.Method != http.MethodGet {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if !bodyless(request) || len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	resource, err := handler.catalog.GetDescriptor(request.Context(), serverID, toolID)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, resource)
}

func parseDescriptorQuery(query url.Values) (int, contract.DescriptorRetiredFilter, *catalog.DescriptorCursor, bool, contract.ProblemCode) {
	for key, values := range query {
		if (key != "cursor" && key != "limit" && key != "retired" && key != "representation") || len(values) != 1 {
			return 0, "", nil, false, contract.ProblemMalformedRequest
		}
	}
	limit := contract.S2ListPageDefault
	if text := query.Get("limit"); text != "" {
		value, err := strconv.Atoi(text)
		if err != nil || value < 1 || value > limitValue("s2_list_page") {
			return 0, "", nil, false, contract.ProblemMalformedRequest
		}
		limit = value
	}
	retired := contract.DescriptorRetiredInclude
	if text, present := query["retired"]; present {
		if len(text) != 1 {
			return 0, "", nil, false, contract.ProblemMalformedRequest
		}
		parsed, err := contract.ParseDescriptorRetiredFilter(text[0])
		if err != nil {
			return 0, "", nil, false, contract.ProblemMalformedRequest
		}
		retired = parsed
	}
	var cursor *catalog.DescriptorCursor
	if text := query.Get("cursor"); text != "" {
		if len(text) > limitValue("cursor_bytes") {
			return 0, "", nil, false, contract.ProblemInvalidCursor
		}
		contents, err := base64.RawURLEncoding.DecodeString(text)
		var decoded catalog.DescriptorCursor
		if err != nil || strictjson.Decode(contents, &decoded, strictjson.Options{MaxBytes: int64(limitValue("cursor_bytes")), MaxDepth: 8, RejectUnknownMembers: true}) != nil {
			return 0, "", nil, false, contract.ProblemInvalidCursor
		}
		cursor = &decoded
	}
	representation := query.Get("representation")
	if representation != "" && representation != "summary" {
		return 0, "", nil, false, contract.ProblemMalformedRequest
	}
	return limit, retired, cursor, representation == "summary", ""
}

func encodeDescriptorCursor(cursor catalog.DescriptorCursor) string {
	contents, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(contents)
}
