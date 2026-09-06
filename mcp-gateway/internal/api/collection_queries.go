package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type AuthorizationCollectionService interface {
	QueryPrincipals(context.Context, authorization.CollectionQuery, *authorization.SnapshotCursor, int) (authorization.PrincipalPage, error)
	QueryGrants(context.Context, authorization.CollectionQuery, *authorization.SnapshotCursor, int) (authorization.GrantTablePage, error)
}

func parseAuthorizationCollectionQuery(raw, collection string) (authorization.CollectionQuery, string, bool, contract.ProblemCode) {
	query := authorization.CollectionQuery{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, "", false, contract.ProblemMalformedRequest
	}
	fields := map[string]*string{"sort": &query.Sort, "direction": &query.Direction, "state": &query.State}
	if collection == "principals" {
		fields["name"], fields["visibility"] = &query.Name, &query.Visibility
	} else {
		fields["identity"], fields["principal"], fields["target"], fields["effect"], fields["representation"] = &query.Identity, &query.Principal, &query.Target, &query.Effect, &query.Representation
		query.PrincipalID, query.ServerID = values.Get("principal_id"), values.Get("server_id")
	}
	enabled := false
	for key, destination := range fields {
		members, exists := values[key]
		if !exists {
			continue
		}
		enabled = true
		if len(members) != 1 || members[0] == "" || members[0] == "null" {
			return query, "", true, contract.ProblemMalformedRequest
		}
		*destination = members[0]
		values.Del(key)
	}
	if !query.Validate(collection) {
		return query, "", enabled, contract.ProblemMalformedRequest
	}
	return query, values.Encode(), enabled, ""
}

func (handler *Handler) queryPrincipals(writer http.ResponseWriter, request *http.Request, query authorization.CollectionQuery, legacy string) {
	limit, cursor, problem := parsePrincipalQuery(legacy)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	if handler.collections == nil {
		writeProblem(writer, contract.ProblemAuthorizationUnavailable)
		return
	}
	page, err := handler.collections.QueryPrincipals(request.Context(), query, cursor, limit)
	if err != nil {
		writePrincipalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.Principal]{Items: page.Items, NextCursor: nextAuthorizationCursor(page.Next)})
}

func (handler *Handler) queryGrants(writer http.ResponseWriter, request *http.Request, query authorization.CollectionQuery, legacy string) {
	limit, _, cursor, problem := parseGrantQuery(legacy)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	if handler.collections == nil {
		writeProblem(writer, contract.ProblemAuthorizationUnavailable)
		return
	}
	page, err := handler.collections.QueryGrants(request.Context(), query, cursor, limit)
	if err != nil {
		writeGrantError(writer, err)
		return
	}
	next := nextAuthorizationCursor(page.Next)
	if query.Representation == "table" {
		writeJSONUnescaped(writer, http.StatusOK, contract.Collection[contract.GrantTableItem]{Items: page.Items, NextCursor: next})
		return
	}
	items := make([]contract.Grant, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, item.Grant)
	}
	writeJSONUnescaped(writer, http.StatusOK, contract.Collection[contract.Grant]{Items: items, NextCursor: next})
}

func nextAuthorizationCursor(cursor *authorization.SnapshotCursor) *string {
	if cursor == nil {
		return nil
	}
	encoded := encodePrincipalCursor(*cursor)
	return &encoded
}
