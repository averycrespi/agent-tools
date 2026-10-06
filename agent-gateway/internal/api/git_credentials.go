package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type GitCredentialService interface {
	Query(context.Context, authorization.GitCollectionQuery, string, int) (contract.QueryCollection[contract.GitCredential], error)
	Get(context.Context, string) (contract.GitCredential, error)
	Create(context.Context, contract.GitCredentialDefinition, []byte) (contract.GitCredential, error)
	Update(context.Context, string, string, contract.GitCredentialDefinition) (contract.GitCredential, error)
	Rotate(context.Context, string, string, []byte) (contract.GitCredential, error)
	Delete(context.Context, string, string) error
}

func (h *Handler) gitCredential(w http.ResponseWriter, r *http.Request, id string, rotate bool) {
	if r.Method == http.MethodGet && id == "" {
		gitCollection(w, r, "git_credentials", func(q authorization.GitCollectionQuery, cursor string, limit int) (contract.QueryCollection[contract.GitCredential], error) {
			return h.gitCredentials.Query(r.Context(), q, cursor, limit)
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
		c, err := h.gitCredentials.Get(r.Context(), id)
		if err != nil {
			writeGitCredentialError(w, err)
			return
		}
		w.Header().Set("ETag", gitETag("credential", c.ID, c.Revision))
		writeJSON(w, http.StatusOK, c)
		return
	}
	revision := ""
	if id != "" {
		var ok bool
		revision, ok = gitPrecondition(w, r, "credential", id)
		if !ok {
			return
		}
	}
	if r.Method == http.MethodDelete {
		var input map[string]json.RawMessage
		if !decodeStrictBody(w, r, &input) {
			return
		}
		if input == nil || len(input) != 0 {
			writeProblem(w, contract.ProblemInvalidJSON)
			return
		}
		if err := h.gitCredentials.Delete(r.Context(), id, revision); err != nil {
			writeGitCredentialError(w, err)
			return
		}
		h.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if rotate {
		var input struct {
			Secret json.RawMessage `json:"secret"`
		}
		if !decodeStrictBody(w, r, &input) {
			return
		}
		defer clear(input.Secret)
		secret, ok := decodeHTTPSecret(w, input.Secret)
		if !ok {
			return
		}
		defer clear(secret)
		c, err := h.gitCredentials.Rotate(r.Context(), id, revision, secret)
		h.gitCredentialResult(w, c, err, http.StatusOK)
		return
	}
	if id != "" {
		var input struct {
			Name   *string                        `json:"name"`
			Origin *string                        `json:"origin"`
			Recipe *contract.HTTPCredentialRecipe `json:"recipe"`
		}
		if !decodeStrictBody(w, r, &input) {
			return
		}
		if input.Name == nil || input.Origin == nil || input.Recipe == nil {
			writeProblem(w, contract.ProblemInvalidOperation)
			return
		}
		c, err := h.gitCredentials.Update(r.Context(), id, revision, contract.GitCredentialDefinition{Name: *input.Name, Origin: *input.Origin, Recipe: *input.Recipe})
		h.gitCredentialResult(w, c, err, http.StatusOK)
		return
	}
	var input struct {
		Name   *string                        `json:"name"`
		Origin *string                        `json:"origin"`
		Recipe *contract.HTTPCredentialRecipe `json:"recipe"`
		Secret json.RawMessage                `json:"secret"`
	}
	if !decodeStrictBody(w, r, &input) {
		return
	}
	defer clear(input.Secret)
	if input.Name == nil || input.Origin == nil || input.Recipe == nil || input.Secret == nil {
		writeProblem(w, contract.ProblemInvalidOperation)
		return
	}
	secret, ok := decodeHTTPSecret(w, input.Secret)
	if !ok {
		return
	}
	defer clear(secret)
	c, err := h.gitCredentials.Create(r.Context(), contract.GitCredentialDefinition{Name: *input.Name, Origin: *input.Origin, Recipe: *input.Recipe}, secret)
	h.gitCredentialResult(w, c, err, http.StatusCreated)
}
func (h *Handler) gitCredentialResult(w http.ResponseWriter, c contract.GitCredential, err error, status int) {
	if err != nil {
		writeGitCredentialError(w, err)
		return
	}
	w.Header().Set("ETag", gitETag("credential", c.ID, c.Revision))
	h.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	writeJSON(w, status, c)
}
func writeGitCredentialError(w http.ResponseWriter, err error) {
	problem := contract.ProblemStorageUnavailable
	var capability *keyring.CapabilityError
	switch {
	case errors.Is(err, storage.ErrStorageLatched):
		problem = contract.ProblemStorageUnavailable
	case errors.Is(err, gitcredentials.ErrInvalid):
		problem = contract.ProblemInvalidOperation
	case errors.Is(err, gitcredentials.ErrNotFound):
		problem = contract.ProblemNotFound
	case errors.Is(err, gitcredentials.ErrStale):
		problem = contract.ProblemStaleRevision
	case errors.Is(err, gitcredentials.ErrReferenced):
		problem = contract.ProblemConflict
	case errors.Is(err, gitcredentials.ErrLimit), errors.Is(err, storage.ErrMutationBusy):
		problem = contract.ProblemResourceLimit
	case errors.As(err, &capability), errors.Is(err, keyring.ErrWorkLimit), errors.Is(err, keyring.ErrNoAuthority), errors.Is(err, keyring.ErrNotFound), errors.Is(err, keyring.ErrCandidateLimit), errors.Is(err, keyring.ErrHandleCollision), errors.Is(err, keyring.ErrDraining), errors.Is(err, keyring.ErrSecretTooLarge), errors.Is(err, keyring.ErrIncompleteGeneration), errors.Is(err, gitcredentials.ErrUnavailable):
		problem = contract.ProblemKeyringUnavailable
	}
	writeProblem(w, problem)
}
