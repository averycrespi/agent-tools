package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func gitCollection[T any](w http.ResponseWriter, r *http.Request, kind string, load func(authorization.GitCollectionQuery, string, int) (contract.QueryCollection[T], error)) {
	if !bodyless(r) {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	q, cursor, limit, problem := parseGitCollectionQuery(r.URL.RawQuery, kind)
	if problem != "" {
		writeProblem(w, problem)
		return
	}
	page, err := load(q, cursor, limit)
	if errors.Is(err, authorization.ErrInvalidGitCursor) {
		writeProblem(w, contract.ProblemInvalidCursor)
		return
	}
	if errors.Is(err, authorization.ErrStaleCursor) {
		writeProblem(w, contract.ProblemStaleCursor)
		return
	}
	if err != nil {
		if kind == "git_credentials" {
			writeGitCredentialError(w, err)
		} else {
			writeGitPolicyError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func parseGitCollectionQuery(raw, kind string) (authorization.GitCollectionQuery, string, int, contract.ProblemCode) {
	q := authorization.GitCollectionQuery{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return q, "", 0, contract.ProblemMalformedRequest
	}
	fields := map[string]*string{"sort": &q.Sort, "direction": &q.Direction}
	switch kind {
	case "git_repositories":
		fields["name"], fields["destination"], fields["credential"] = &q.Name, &q.Destination, &q.Credential
	case "git_grants":
		fields["identity"], fields["principal"], fields["repository"], fields["state"] = &q.Identity, &q.Principal, &q.Repository, &q.State
	case "git_credentials":
		fields["name"], fields["origin"], fields["status"] = &q.Name, &q.Origin, &q.Status
	}
	limit := contract.CollectionPageDefault
	for key, members := range values {
		if len(members) != 1 || members[0] == "" || members[0] == "null" {
			return q, "", 0, contract.ProblemMalformedRequest
		}
		if destination, ok := fields[key]; ok {
			*destination = members[0]
		} else if key == "limit" {
			n, e := strconv.Atoi(members[0])
			if e != nil || n < 1 || n > 100 || strconv.Itoa(n) != members[0] {
				return q, "", 0, contract.ProblemMalformedRequest
			}
			limit = n
		} else if key != "cursor" {
			return q, "", 0, contract.ProblemMalformedRequest
		}
	}
	if !q.Validate(kind) {
		return q, "", 0, contract.ProblemMalformedRequest
	}
	return q, values.Get("cursor"), limit, ""
}
