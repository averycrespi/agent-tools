package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type CredentialReplacementService interface {
	Prepare(context.Context, serverdomain.CredentialReplacementRequest) (serverdomain.CredentialReplacementPlan, error)
	Replace(context.Context, serverdomain.CredentialReplacementPlan, []byte) (serverdomain.CredentialReplacementPublication, error)
}

type rawCredentialReplacement struct {
	Kind             contract.ServerCredentialKind `json:"kind"`
	ExpectedRevision string                        `json:"expected_revision"`
	Values           json.RawMessage               `json:"values"`
	ClientSecret     json.RawMessage               `json:"client_secret"`
}

func (handler *Handler) credentialReplacements(writer http.ResponseWriter, request *http.Request, serverID string) {
	if request.Method != http.MethodPost {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	var raw rawCredentialReplacement
	options := strictjson.Options{MaxBytes: int64(limitValue("api_json_body_bytes")), MaxDepth: int(limitValue("json_depth")), RejectUnknownMembers: true}
	if err := strictjson.DecodeReader(request.Body, &raw, options); err != nil {
		writeProblem(writer, contract.ProblemInvalidJSON)
		return
	}
	defer clear(raw.Values)
	defer clear(raw.ClientSecret)
	desiredRevision, ok := serverPrecondition(writer, request, serverID)
	if !ok {
		return
	}
	if _, err := contract.ParseCredentialReplacementKind(string(raw.Kind)); err != nil || raw.ExpectedRevision == "" {
		writeProblem(writer, contract.ProblemInvalidOperation)
		return
	}

	var slots []string
	switch raw.Kind {
	case contract.ServerCredentialStatic:
		if raw.Values == nil || raw.ClientSecret != nil || int64(len(raw.Values)) > int64(limitValue("keyring_secret_bytes")) {
			writeProblem(writer, contract.ProblemInvalidOperation)
			return
		}
		var encodedValues map[string]json.RawMessage
		if strictjson.Decode(raw.Values, &encodedValues, strictjson.Options{MaxBytes: int64(limitValue("keyring_secret_bytes")), MaxDepth: int(limitValue("json_depth"))}) != nil {
			writeProblem(writer, contract.ProblemInvalidJSON)
			return
		}
		for slot := range encodedValues {
			slots = append(slots, slot)
		}
	case contract.ServerCredentialOAuthClient:
		if raw.ClientSecret == nil || raw.Values != nil || int64(len(raw.ClientSecret)) > int64(limitValue("keyring_secret_bytes")) {
			writeProblem(writer, contract.ProblemInvalidOperation)
			return
		}
	}
	plan, err := handler.replacements.Prepare(request.Context(), serverdomain.CredentialReplacementRequest{
		ServerID: serverID, Kind: raw.Kind, ExpectedDesiredRevision: desiredRevision,
		ExpectedCredentialRevision: raw.ExpectedRevision, Slots: slots,
	})
	if err != nil {
		writeCredentialReplacementError(writer, err)
		return
	}

	var secret []byte
	switch raw.Kind {
	case contract.ServerCredentialStatic:
		var values map[string]string
		if strictjson.Decode(raw.Values, &values, strictjson.Options{MaxBytes: int64(limitValue("keyring_secret_bytes")), MaxDepth: int(limitValue("json_depth"))}) != nil {
			writeProblem(writer, contract.ProblemInvalidJSON)
			return
		}
		secret, err = servercredentials.EncodeStaticGeneration(values)
	case contract.ServerCredentialOAuthClient:
		var clientSecret string
		if strictjson.Decode(raw.ClientSecret, &clientSecret, strictjson.Options{MaxBytes: int64(limitValue("keyring_secret_bytes")), MaxDepth: 1}) != nil || clientSecret == "" || !utf8.ValidString(clientSecret) || int64(len(clientSecret)) > int64(limitValue("oauth_client_secret_bytes")) {
			err = servercredentials.ErrInvalidSecret
		} else {
			secret = []byte(clientSecret)
		}
	}
	if err != nil {
		writeProblem(writer, contract.ProblemInvalidOperation)
		return
	}
	publication, replaceErr := handler.replacements.Replace(request.Context(), plan, secret)
	if publication.Operation.ID != "" {
		handler.emit(contract.Invalidation{Kind: contract.InvalidationServers, ResourceID: &serverID})
		handler.emit(contract.Invalidation{Kind: contract.InvalidationServerOperations, ResourceID: &publication.Operation.ID})
		handler.trigger(serverID, &publication.Operation.ID, true)
	}
	if replaceErr != nil {
		writeCredentialReplacementError(writer, replaceErr)
		return
	}
	writeJSON(writer, http.StatusAccepted, contract.CredentialReplacementResult{
		ServerID: serverID, Kind: raw.Kind, CredentialRevision: publication.Revision,
		Operation: *operationResource(&publication.Operation),
	})
}

func writeCredentialReplacementError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, serverdomain.ErrNotFound):
		writeProblem(writer, contract.ProblemNotFound)
	case errors.Is(err, serverdomain.ErrStaleRevision):
		writeProblem(writer, contract.ProblemStaleRevision)
	case errors.Is(err, serverdomain.ErrOperationConflict):
		writeProblem(writer, contract.ProblemOperationConflict)
	case errors.Is(err, serverdomain.ErrInvalidInput), errors.Is(err, serverdomain.ErrInvalidOperation), errors.Is(err, servercredentials.ErrInvalidSecret):
		writeProblem(writer, contract.ProblemInvalidOperation)
	case errors.Is(err, keyring.ErrWorkLimit):
		writeProblem(writer, contract.ProblemResourceLimit)
	default:
		writeProblem(writer, contract.ProblemKeyringUnavailable)
	}
}
