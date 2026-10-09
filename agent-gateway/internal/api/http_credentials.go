package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type HTTPCredentialService interface {
	List(context.Context) ([]httpcredentials.Resource, error)
	Get(context.Context, string) (httpcredentials.Resource, error)
	Create(context.Context, httpcredentials.Definition, []byte) (httpcredentials.Resource, error)
	Update(context.Context, string, string, httpcredentials.Definition) (httpcredentials.Resource, error)
	Rotate(context.Context, string, string, []byte) (httpcredentials.Resource, error)
	Delete(context.Context, string, string) error
}

type httpCredentialInput struct {
	Name     *string                        `json:"name"`
	Boundary *httpcredentials.Boundary      `json:"boundary"`
	Recipe   *contract.HTTPCredentialRecipe `json:"recipe"`
	Secret   json.RawMessage                `json:"secret"`
}

func (h *Handler) httpCredentialCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if r.ContentLength != 0 {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		selection := httpcredentials.CollectionQuery{}
		fields := map[string]*string{"name": &selection.Name, "boundary": &selection.Boundary, "recipe": &selection.Recipe, "status": &selection.Status, "sort": &selection.Sort, "direction": &selection.Direction}
		for key, destination := range fields {
			if members, exists := query[key]; exists {
				if len(members) != 1 || members[0] == "" {
					writeProblem(w, contract.ProblemMalformedRequest)
					return
				}
				*destination = members[0]
				query.Del(key)
			}
		}
		if !selection.Validate() {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		cursor := query.Get("cursor")
		if members, exists := query["cursor"]; exists && (len(members) != 1 || cursor == "") {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		query.Del("cursor")
		limit, _, problem := parseCollectionQuery(query, "http_credentials")
		if problem != "" {
			writeProblem(w, problem)
			return
		}
		items, err := h.httpCredentials.List(r.Context())
		if err != nil {
			writeHTTPCredentialError(w, err)
			return
		}
		page, err := httpcredentials.SelectPage(items, selection, cursor, limit, time.Now(), h.httpCredentialCursorKey)
		if err != nil {
			switch {
			case errors.Is(err, httpcredentials.ErrInvalidCursor):
				writeProblem(w, contract.ProblemInvalidCursor)
			case errors.Is(err, httpcredentials.ErrStaleCursor):
				writeProblem(w, contract.ProblemStaleCursor)
			default:
				writeHTTPCredentialError(w, err)
			}
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	if r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	var input httpCredentialInput
	if !decodeStrictBody(w, r, &input) {
		return
	}
	defer clear(input.Secret)
	if input.Name == nil || input.Boundary == nil || input.Recipe == nil || input.Secret == nil {
		writeProblem(w, contract.ProblemInvalidOperation)
		return
	}
	secret, ok := decodeHTTPSecret(w, input.Secret)
	if !ok {
		return
	}
	defer clear(secret)
	resource, err := h.httpCredentials.Create(r.Context(), httpcredentials.Definition{Name: *input.Name, Boundary: *input.Boundary, Recipe: *input.Recipe}, secret)
	h.httpCredentialResult(w, resource, err, http.StatusCreated)
}

func (h *Handler) httpCredentialMember(w http.ResponseWriter, r *http.Request, id string, rotate bool) {
	if !contract.ValidAuditID(id) {
		writeProblem(w, contract.ProblemNotFound)
		return
	}
	if r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	if r.Method == http.MethodGet && !rotate {
		if r.ContentLength != 0 {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		resource, err := h.httpCredentials.Get(r.Context(), id)
		if err != nil {
			writeHTTPCredentialError(w, err)
			return
		}
		w.Header().Set("ETag", contract.HTTPCredentialETag(id, resource.Revision))
		writeJSON(w, http.StatusOK, resource)
		return
	}
	revision, ok := httpCredentialPrecondition(w, r, id)
	if !ok {
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
		resource, err := h.httpCredentials.Rotate(r.Context(), id, revision, secret)
		h.httpCredentialResult(w, resource, err, http.StatusOK)
		return
	}
	if r.Method == http.MethodDelete {
		if !decodeEmptyObject(w, r) {
			return
		}
		if err := h.httpCredentials.Delete(r.Context(), id, revision); err != nil {
			writeHTTPCredentialError(w, err)
			return
		}
		h.emit(contract.Invalidation{Kind: contract.InvalidationHTTPCredentials})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var input struct {
		Name     *string                        `json:"name"`
		Boundary *httpcredentials.Boundary      `json:"boundary"`
		Recipe   *contract.HTTPCredentialRecipe `json:"recipe"`
	}
	if !decodeStrictBody(w, r, &input) {
		return
	}
	if input.Name == nil || input.Boundary == nil || input.Recipe == nil {
		writeProblem(w, contract.ProblemInvalidOperation)
		return
	}
	resource, err := h.httpCredentials.Update(r.Context(), id, revision, httpcredentials.Definition{Name: *input.Name, Boundary: *input.Boundary, Recipe: *input.Recipe})
	h.httpCredentialResult(w, resource, err, http.StatusOK)
}

func decodeHTTPSecret(w http.ResponseWriter, raw json.RawMessage) ([]byte, bool) {
	var value string
	if len(raw) > contract.HTTPCredentialValueBytes*6+2 || json.Unmarshal(raw, &value) != nil || value == "" {
		writeProblem(w, contract.ProblemInvalidOperation)
		return nil, false
	}
	return []byte(value), true
}

func httpCredentialPrecondition(w http.ResponseWriter, r *http.Request, id string) (string, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		writeProblem(w, contract.ProblemPreconditionRequired)
		return "", false
	}
	if len(values) != 1 {
		writeProblem(w, contract.ProblemStaleRevision)
		return "", false
	}
	prefix := `"http-credential-` + id + `-`
	value := values[0]
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		writeProblem(w, contract.ProblemStaleRevision)
		return "", false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	n, err := strconv.ParseUint(revision, 10, 63)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != revision {
		writeProblem(w, contract.ProblemStaleRevision)
		return "", false
	}
	return revision, true
}

func (h *Handler) httpCredentialResult(w http.ResponseWriter, resource httpcredentials.Resource, err error, status int) {
	if err != nil {
		writeHTTPCredentialError(w, err)
		return
	}
	w.Header().Set("ETag", contract.HTTPCredentialETag(resource.ID, resource.Revision))
	h.emit(contract.Invalidation{Kind: contract.InvalidationHTTPCredentials})
	writeJSON(w, status, resource)
}
func writeHTTPCredentialError(w http.ResponseWriter, err error) {
	problem := contract.ProblemStorageUnavailable
	var capability *keyring.CapabilityError
	switch {
	case errors.Is(err, httpcredentials.ErrInvalid):
		problem = contract.ProblemInvalidOperation
	case errors.Is(err, httpcredentials.ErrNotFound):
		problem = contract.ProblemNotFound
	case errors.Is(err, httpcredentials.ErrStale):
		problem = contract.ProblemStaleRevision
	case errors.Is(err, httpcredentials.ErrReferenced):
		problem = contract.ProblemConflict
	case errors.Is(err, httpcredentials.ErrLimit), errors.Is(err, storage.ErrMutationBusy):
		problem = contract.ProblemResourceLimit
	case errors.Is(err, storage.ErrStorageLatched):
		problem = contract.ProblemStorageUnavailable
	case errors.As(err, &capability), errors.Is(err, keyring.ErrWorkLimit), errors.Is(err, keyring.ErrNoAuthority), errors.Is(err, keyring.ErrNotFound), errors.Is(err, keyring.ErrCandidateLimit), errors.Is(err, keyring.ErrHandleCollision), errors.Is(err, keyring.ErrDraining), errors.Is(err, keyring.ErrSecretTooLarge), errors.Is(err, keyring.ErrIncompleteGeneration), errors.Is(err, keyring.ErrCustodyUnavailable), errors.Is(err, httpcredentials.ErrUnavailable):
		problem = contract.ProblemKeyringUnavailable
	}
	writeProblem(w, problem)
}
