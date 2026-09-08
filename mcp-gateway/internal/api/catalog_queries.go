package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type DescriptorQueryService interface {
	QueryDescriptors(context.Context, string, catalog.ToolQuery, *catalog.DescriptorCursor, int) (catalog.DescriptorPage, error)
}

func parseToolQuery(raw string, aggregate bool) (catalog.ToolQuery, url.Values, bool, contract.ProblemCode) {
	query := catalog.ToolQuery{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, nil, false, contract.ProblemMalformedRequest
	}
	fields := map[string]*string{"tool": &query.Tool, "status": &query.Status, "sort": &query.Sort, "direction": &query.Direction}
	if aggregate {
		fields["server"] = &query.Server
	}
	enabled := false
	for key, destination := range fields {
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
	if !query.Validate(aggregate) {
		return query, nil, enabled, contract.ProblemMalformedRequest
	}
	if enabled {
		for key, members := range values {
			if (key != "cursor" && key != "limit") || len(members) != 1 || members[0] == "" {
				return query, nil, true, contract.ProblemMalformedRequest
			}
		}
		if text, exists := values["limit"]; exists {
			limit, err := strconv.Atoi(text[0])
			if err != nil || limit < 1 || limit > contract.S2ListPageDefault || strconv.Itoa(limit) != text[0] {
				return query, nil, true, contract.ProblemMalformedRequest
			}
		}
	}
	return query, values, enabled, ""
}

func (handler *Handler) queryDescriptors(writer http.ResponseWriter, request *http.Request, serverID string, query catalog.ToolQuery, legacy url.Values) {
	limit, _, cursor, _, problem := parseDescriptorQuery(legacy)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	service, ok := handler.catalog.(DescriptorQueryService)
	if !ok {
		writeProblem(writer, contract.ProblemStorageUnavailable)
		return
	}
	page, err := service.QueryDescriptors(request.Context(), serverID, query, cursor, limit)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	items := make([]contract.ToolDescriptor, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, item.Resource)
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.ToolDescriptor]{Items: items, NextCursor: encodedDescriptorCursor(page.Next)})
}
