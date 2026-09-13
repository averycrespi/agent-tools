package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type AuthorizationCollectionService interface {
	QueryPrincipals(context.Context, authorization.CollectionQuery, *authorization.SnapshotCursor, int) (authorization.PrincipalPage, error)
	QueryGrants(context.Context, authorization.CollectionQuery, *authorization.SnapshotCursor, int) (authorization.GrantTablePage, error)
}

func parseAuthorizationCollectionQuery(raw, collection string) (authorization.CollectionQuery, string, contract.ProblemCode) {
	query := authorization.CollectionQuery{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, "", contract.ProblemMalformedRequest
	}
	fields := map[string]*string{"sort": &query.Sort, "direction": &query.Direction, "state": &query.State}
	if collection == "principals" {
		fields["name"], fields["visibility"] = &query.Name, &query.Visibility
	} else {
		fields["identity"], fields["principal"], fields["target"], fields["effect"] = &query.Identity, &query.Principal, &query.Target, &query.Effect
		query.PrincipalID, query.ServerID = values.Get("principal_id"), values.Get("server_id")
	}
	for key, destination := range fields {
		members, exists := values[key]
		if !exists {
			continue
		}
		if len(members) != 1 || members[0] == "" || members[0] == "null" {
			return query, "", contract.ProblemMalformedRequest
		}
		*destination = members[0]
		values.Del(key)
	}
	if !query.Validate(collection) {
		return query, "", contract.ProblemMalformedRequest
	}
	if query.Sort == "" {
		query.Sort = "name"
		if collection == "grants" {
			query.Sort = "description"
		}
	}
	if query.Direction == "" {
		query.Direction = "ascending"
	}
	return query, values.Encode(), ""
}

func (handler *Handler) queryPrincipals(writer http.ResponseWriter, request *http.Request, query authorization.CollectionQuery, pagination string) {
	limit, cursor, problem := parsePrincipalQuery(pagination)
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
	writeJSON(writer, http.StatusOK, contract.QueryCollection[contract.Principal]{Collection: contract.Collection[contract.Principal]{Items: page.Items, NextCursor: nextAuthorizationCursor(page.Next)}, CollectionRange: page.CollectionRange})
}

func (handler *Handler) queryGrants(writer http.ResponseWriter, request *http.Request, query authorization.CollectionQuery, pagination string) {
	limit, _, cursor, problem := parseGrantQuery(pagination)
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
	writeJSONUnescaped(writer, http.StatusOK, contract.QueryCollection[contract.GrantTableItem]{Collection: contract.Collection[contract.GrantTableItem]{Items: page.Items, NextCursor: next}, CollectionRange: page.CollectionRange})
}

func nextAuthorizationCursor(cursor *authorization.SnapshotCursor) *string {
	if cursor == nil {
		return nil
	}
	encoded := encodePrincipalCursor(*cursor)
	return &encoded
}
