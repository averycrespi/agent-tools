package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
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
	query, values, problem := parseToolQuery(request.URL.RawQuery, false)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	handler.queryDescriptors(writer, request, serverID, query, values)
}

func encodedDescriptorCursor(cursor *catalog.DescriptorCursor) *string {
	if cursor == nil {
		return nil
	}
	value := encodeDescriptorCursor(*cursor)
	return &value
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

func parseDescriptorQuery(query url.Values) (int, *catalog.DescriptorCursor, contract.ProblemCode) {
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 || values[0] == "" {
			return 0, nil, contract.ProblemMalformedRequest
		}
	}
	limit := contract.CollectionPageDefault
	if text := query.Get("limit"); text != "" {
		value, err := strconv.Atoi(text)
		if err != nil || value < 1 || value > contract.CatalogPageMaximum || strconv.Itoa(value) != text {
			return 0, nil, contract.ProblemMalformedRequest
		}
		limit = value
	}
	var cursor *catalog.DescriptorCursor
	if text := query.Get("cursor"); text != "" {
		if len(text) > limitValue("cursor_bytes") {
			return 0, nil, contract.ProblemInvalidCursor
		}
		contents, err := base64.RawURLEncoding.DecodeString(text)
		var decoded catalog.DescriptorCursor
		if err != nil || strictjson.Decode(contents, &decoded, strictjson.Options{MaxBytes: int64(limitValue("cursor_bytes")), MaxDepth: 8, RejectUnknownMembers: true}) != nil {
			return 0, nil, contract.ProblemInvalidCursor
		}
		cursor = &decoded
	}
	return limit, cursor, ""
}

func encodeDescriptorCursor(cursor catalog.DescriptorCursor) string {
	contents, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(contents)
}
