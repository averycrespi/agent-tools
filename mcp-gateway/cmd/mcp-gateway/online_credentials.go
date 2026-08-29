package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var canonicalRevisionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func validCanonicalRevision(value string) bool {
	if !canonicalRevisionPattern.MatchString(value) {
		return false
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func runServerCredentialReplace(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ID is invalid."))
	}
	matches := serverETagPattern.FindStringSubmatch(options.etag)
	if len(matches) != 3 || matches[1] != args[0] {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ETag is invalid or belongs to another server."))
	}
	body, err := controlclient.ReadJSONInput(controlclient.InputOptions{Path: options.file, Stdin: command.InOrStdin(), AllowedMembers: []string{"kind", "expected_revision", "values", "client_secret"}})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	kind, canonical, err := validateCredentialReplacementInput(body)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The credential replacement input is invalid."))
	}
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{
		Yes:         options.yes,
		Consequence: "Replace this server credential, withdraw routes, and interrupt in-flight calls whose outcome may be unknown? Native keyring interaction may fail, prompt, or outlive cancellation.",
	}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	return runCredentialReplacement(command, options, credentialReplacementRequest{serverID: args[0], etag: options.etag, body: canonical, kind: kind})
}

type rawCredentialReplacementInput struct {
	Kind             contract.ServerCredentialKind `json:"kind"`
	ExpectedRevision string                        `json:"expected_revision"`
	Values           json.RawMessage               `json:"values,omitempty"`
	ClientSecret     json.RawMessage               `json:"client_secret,omitempty"`
}

func validateCredentialReplacementInput(body []byte) (contract.ServerCredentialKind, []byte, error) {
	var raw rawCredentialReplacementInput
	if controlclient.DecodeResponse(body, &raw) != nil || !validCanonicalRevision(raw.ExpectedRevision) {
		return "", nil, controlclient.ErrInvalidInput
	}
	kind, err := contract.ParseCredentialReplacementKind(string(raw.Kind))
	if err != nil {
		return "", nil, controlclient.ErrInvalidInput
	}
	switch kind {
	case contract.ServerCredentialStatic:
		if raw.Values == nil || raw.ClientSecret != nil {
			return "", nil, controlclient.ErrInvalidInput
		}
		var values map[string]string
		if controlclient.DecodeResponse(raw.Values, &values) != nil || len(values) == 0 {
			return "", nil, controlclient.ErrInvalidInput
		}
		for slot, value := range values {
			if slot == "" || value == "" || !utf8.ValidString(slot) || !utf8.ValidString(value) {
				return "", nil, controlclient.ErrInvalidInput
			}
		}
		canonical, marshalErr := json.Marshal(contract.StaticCredentialReplacement{Kind: kind, ExpectedRevision: raw.ExpectedRevision, Values: values})
		return kind, canonical, marshalErr
	case contract.ServerCredentialOAuthClient:
		if raw.ClientSecret == nil || raw.Values != nil {
			return "", nil, controlclient.ErrInvalidInput
		}
		var secret string
		if controlclient.DecodeResponse(raw.ClientSecret, &secret) != nil || secret == "" || !utf8.ValidString(secret) {
			return "", nil, controlclient.ErrInvalidInput
		}
		canonical, marshalErr := json.Marshal(contract.OAuthClientCredentialReplacement{Kind: kind, ExpectedRevision: raw.ExpectedRevision, ClientSecret: secret}) //nolint:gosec // The secret is deliberately encoded only into the write-only request body.
		return kind, canonical, marshalErr
	default:
		return "", nil, controlclient.ErrInvalidInput
	}
}

type credentialReplacementRequest struct {
	serverID string
	etag     string
	body     []byte
	kind     contract.ServerCredentialKind
}

func runCredentialReplacement(command *cobra.Command, options *onlineOptions, request credentialReplacementRequest) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, ETag: request.etag})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/servers/" + request.serverID + "/credential-replacements", Header: header, Body: request.body})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = credentialReplacementUncertainTitle(request.serverID)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusAccepted {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: credentialReplacementUncertainTitle(request.serverID), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || len(response.Body) == 0 {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: credentialReplacementUncertainTitle(request.serverID), Exit: 8, Uncertain: true})
	}
	var result contract.CredentialReplacementResult
	var stateErr error
	if controlclient.DecodeResponse(response.Body, &result) != nil {
		stateErr = controlclient.ErrResponseInvalid
	} else {
		_, stateErr = contract.ParseServerOperationState(string(result.Operation.State))
	}
	if stateErr != nil || result.ServerID != request.serverID || result.Kind != request.kind || !validCanonicalRevision(result.CredentialRevision) || result.Operation.ServerID != request.serverID || result.Operation.Kind != contract.OperationCredentialReplace || !gatewayIDPattern.MatchString(result.Operation.ID) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: credentialReplacementUncertainTitle(request.serverID), Exit: 8, Uncertain: true})
	}
	if mode == controlclient.OutputJSON {
		if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{}); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return nil
	}
	table := controlclient.Table{Headers: []string{"SERVER", "KIND", "CREDENTIAL_REVISION", "OPERATION", "STATE"}, Rows: [][]string{{result.ServerID, string(result.Kind), result.CredentialRevision, result.Operation.ID, string(result.Operation.State)}}}
	if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, table); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command output could not be written."))
	}
	return nil
}

func credentialReplacementUncertainTitle(serverID string) string {
	return "The credential replacement outcome is uncertain. Nothing was replayed. Inspect server get " + serverID + " and server operation list " + serverID + "; reads cannot prove which secret became authoritative. Do not submit another replacement until you decide how to recover."
}
