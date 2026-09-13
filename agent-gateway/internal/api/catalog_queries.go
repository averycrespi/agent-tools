package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type DescriptorQueryService interface {
	QueryDescriptors(context.Context, string, catalog.ToolQuery, *catalog.DescriptorCursor, int) (catalog.DescriptorPage, error)
}

func parseToolQuery(raw string, aggregate bool) (catalog.ToolQuery, url.Values, contract.ProblemCode) {
	query := catalog.ToolQuery{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, nil, contract.ProblemMalformedRequest
	}
	fields := map[string]*string{"tool": &query.Tool, "status": &query.Status, "sort": &query.Sort, "direction": &query.Direction}
	if aggregate {
		fields["server"] = &query.Server
	} else {
		fields["projection"] = &query.Projection
	}
	for key, destination := range fields {
		members, exists := values[key]
		if !exists {
			continue
		}
		if len(members) != 1 || members[0] == "" || members[0] == "null" {
			return query, nil, contract.ProblemMalformedRequest
		}
		*destination = members[0]
		values.Del(key)
	}
	if !query.Validate(aggregate) {
		return query, nil, contract.ProblemMalformedRequest
	}
	for key, members := range values {
		if (key != "cursor" && key != "limit") || len(members) != 1 || members[0] == "" {
			return query, nil, contract.ProblemMalformedRequest
		}
	}
	if text, exists := values["limit"]; exists {
		limit, err := strconv.Atoi(text[0])
		if err != nil || limit < 1 || limit > contract.S2ListPageDefault || strconv.Itoa(limit) != text[0] {
			return query, nil, contract.ProblemMalformedRequest
		}
	}
	if query.Sort == "" {
		query.Sort, query.Direction = "tool", "ascending"
		if !aggregate {
			query.Sort, query.Direction = "last-seen", "descending"
		}
	} else if query.Direction == "" {
		query.Direction = "ascending"
	}
	if !aggregate && query.Projection == "" {
		query.Projection = "full"
	}
	return query, values, ""
}

func (handler *Handler) queryDescriptors(writer http.ResponseWriter, request *http.Request, serverID string, query catalog.ToolQuery, pagination url.Values) {
	limit, cursor, problem := parseDescriptorQuery(pagination)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	if cursor != nil && cursor.Epoch != handler.inventoryEpoch {
		writeProblem(writer, contract.ProblemStaleCursor)
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
	if page.Next != nil {
		page.Next.Epoch = handler.inventoryEpoch
	}
	if query.Projection == "summary" {
		items := make([]contract.ToolDescriptorSummary, 0, len(page.Items))
		for _, item := range page.Items {
			resource := item.Resource
			items = append(items, contract.ToolDescriptorSummary{ID: resource.ID, ServerID: resource.ServerID, UpstreamName: resource.UpstreamName, ExternalName: resource.ExternalName, CatalogRevision: resource.CatalogRevision})
		}
		writeJSON(writer, http.StatusOK, contract.Collection[contract.ToolDescriptorSummary]{Items: items, NextCursor: encodedDescriptorCursor(page.Next)})
		return
	}
	items := make([]contract.ToolDescriptor, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, item.Resource)
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.ToolDescriptor]{Items: items, NextCursor: encodedDescriptorCursor(page.Next)})
}
