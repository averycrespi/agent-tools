package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
)

type GitPolicyService interface {
	QueryGitRepositories(context.Context, authorization.GitCollectionQuery, string, int) (contract.QueryCollection[contract.GitRepository], error)
	GetGitRepository(context.Context, string) (contract.GitRepository, error)
	PutGitRepository(context.Context, string, string, contract.GitRepositoryDefinition) (contract.GitRepository, error)
	DeleteGitRepository(context.Context, string, string) error
	QueryGitGrants(context.Context, authorization.GitCollectionQuery, string, int) (contract.QueryCollection[contract.GitGrant], error)
	GetGitGrant(context.Context, string) (contract.GitGrant, error)
	PutGitGrant(context.Context, string, string, authorization.GitGrantInput) (contract.GitGrant, error)
	DeleteGitGrant(context.Context, string, string) error
	GetGitRoutingProfile(context.Context) (contract.GitRoutingProfile, error)
	PutGitRoutingProfile(context.Context, string, []string) (contract.GitRoutingProfile, error)
}

func writeGitPolicyError(w http.ResponseWriter, err error) {
	if errors.Is(err, authorization.ErrStaleRevision) {
		writeProblem(w, contract.ProblemStaleRevision)
		return
	}
	writeGrantError(w, err)
}

func gitETag(kind, id, revision string) string {
	return `"git-` + kind + `-` + id + `-` + revision + `"`
}
func gitPrecondition(w http.ResponseWriter, r *http.Request, kind, id string) (string, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		writeProblem(w, contract.ProblemPreconditionRequired)
		return "", false
	}
	prefix := `"git-` + kind + `-` + id + `-`
	if len(values) != 1 || !strings.HasPrefix(values[0], prefix) || !strings.HasSuffix(values[0], `"`) {
		writeProblem(w, contract.ProblemStaleRevision)
		return "", false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(values[0], prefix), `"`)
	if !gitpolicy.ValidRevision(revision) {
		writeProblem(w, contract.ProblemStaleRevision)
		return "", false
	}
	return revision, true
}

func (h *Handler) gitRepositories(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet && id == "" {
		gitCollection(w, r, "git_repositories", func(q authorization.GitCollectionQuery, cursor string, limit int) (contract.QueryCollection[contract.GitRepository], error) {
			return h.gitPolicies.QueryGitRepositories(r.Context(), q, cursor, limit)
		})
		return
	}
	if r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	if id != "" && !contract.ValidAuditID(id) {
		writeProblem(w, contract.ProblemNotFound)
		return
	}
	if r.Method == http.MethodGet {
		if !bodyless(r) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		g, err := h.gitPolicies.GetGitRepository(r.Context(), id)
		if err != nil {
			writeGitPolicyError(w, err)
			return
		}
		w.Header().Set("ETag", gitETag("repository", id, g.Revision))
		writeJSON(w, http.StatusOK, g)
		return
	}
	revision := ""
	if id != "" {
		var ok bool
		revision, ok = gitPrecondition(w, r, "repository", id)
		if !ok {
			return
		}
	}
	if r.Method == http.MethodDelete {
		if !bodyless(r) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		if err := h.gitPolicies.DeleteGitRepository(r.Context(), id, revision); err != nil {
			writeGitPolicyError(w, err)
			return
		}
		h.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var input struct {
		Name         *string         `json:"name"`
		URL          *string         `json:"url"`
		Aliases      *[]string       `json:"aliases"`
		CredentialID json.RawMessage `json:"credential_id"`
	}
	if !decodeStrictBody(w, r, &input) {
		return
	}
	var credential *string
	if input.Name == nil || input.URL == nil || input.Aliases == nil || !decodeNullableGrantMember(input.CredentialID, &credential) {
		writeProblem(w, contract.ProblemInvalidGrant)
		return
	}
	g, err := h.gitPolicies.PutGitRepository(r.Context(), id, revision, contract.GitRepositoryDefinition{Name: *input.Name, URL: *input.URL, Aliases: *input.Aliases, CredentialID: credential})
	if err != nil {
		writeGitPolicyError(w, err)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	w.Header().Set("ETag", gitETag("repository", g.ID, g.Revision))
	h.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	writeJSON(w, status, g)
}

func (h *Handler) gitGrants(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet && id == "" {
		gitCollection(w, r, "git_grants", func(q authorization.GitCollectionQuery, cursor string, limit int) (contract.QueryCollection[contract.GitGrant], error) {
			return h.gitPolicies.QueryGitGrants(r.Context(), q, cursor, limit)
		})
		return
	}
	if r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	if id != "" && !contract.ValidAuditID(id) {
		writeProblem(w, contract.ProblemNotFound)
		return
	}
	if r.Method == http.MethodGet {
		if !bodyless(r) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		g, err := h.gitPolicies.GetGitGrant(r.Context(), id)
		if err != nil {
			writeGitPolicyError(w, err)
			return
		}
		w.Header().Set("ETag", gitETag("grant", id, g.Revision))
		writeJSON(w, http.StatusOK, g)
		return
	}
	revision := ""
	if id != "" {
		var ok bool
		revision, ok = gitPrecondition(w, r, "grant", id)
		if !ok {
			return
		}
	}
	if r.Method == http.MethodDelete {
		if !bodyless(r) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		if err := h.gitPolicies.DeleteGitGrant(r.Context(), id, revision); err != nil {
			writeGitPolicyError(w, err)
			return
		}
		h.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var raw struct {
		PrincipalID  json.RawMessage `json:"principal_id"`
		RepositoryID json.RawMessage `json:"repository_id"`
		Description  json.RawMessage `json:"description"`
		Policy       json.RawMessage `json:"policy"`
		ExpiresAt    json.RawMessage `json:"expires_at"`
	}
	if !decodeStrictBody(w, r, &raw) {
		return
	}
	var input authorization.GitGrantInput
	var expiry *string
	if !decodeRequiredGrantMember(raw.PrincipalID, &input.PrincipalID) || !decodeRequiredGrantMember(raw.RepositoryID, &input.RepositoryID) || !decodeNullableGrantMember(raw.Description, &input.Description) || !decodeNullableGrantMember(raw.ExpiresAt, &expiry) || raw.Policy == nil {
		writeProblem(w, contract.ProblemInvalidGrant)
		return
	}
	if expiry != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *expiry)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != *expiry {
			writeProblem(w, contract.ProblemInvalidGrant)
			return
		}
		input.ExpiresAt = &parsed
	}
	input.Policy = raw.Policy
	g, err := h.gitPolicies.PutGitGrant(r.Context(), id, revision, input)
	if err != nil {
		writeGitPolicyError(w, err)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	w.Header().Set("ETag", gitETag("grant", g.ID, g.Revision))
	h.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	writeJSON(w, status, g)
}

func (h *Handler) gitProfile(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	if r.Method == http.MethodGet {
		if !bodyless(r) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		g, err := h.gitPolicies.GetGitRoutingProfile(r.Context())
		if err != nil {
			writeGitPolicyError(w, err)
			return
		}
		w.Header().Set("ETag", gitETag("profile", "routing", g.Revision))
		writeJSON(w, http.StatusOK, g)
		return
	}
	revision, ok := gitPrecondition(w, r, "profile", "routing")
	if !ok {
		return
	}
	var input struct {
		Origins *[]string `json:"origins"`
	}
	if !decodeStrictBody(w, r, &input) {
		return
	}
	if input.Origins == nil {
		writeProblem(w, contract.ProblemInvalidGrant)
		return
	}
	g, err := h.gitPolicies.PutGitRoutingProfile(r.Context(), revision, *input.Origins)
	if err != nil {
		writeGitPolicyError(w, err)
		return
	}
	w.Header().Set("ETag", gitETag("profile", "routing", g.Revision))
	h.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	writeJSON(w, http.StatusOK, g)
}
