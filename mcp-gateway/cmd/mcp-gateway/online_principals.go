package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var principalETagPattern = regexp.MustCompile(`^"principal-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)

func runPrincipalCreate(command *cobra.Command, options *onlineOptions) error {
	body, members, err := readPrincipalInput(command, options, true)
	if err != nil || !members["display_name"] || !members["visibility"] || len(members) != 2 {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The principal create input is invalid."))
	}
	return runPrincipalMutation(command, options, principalMutationRequest{method: http.MethodPost, path: "/api/v1/principals", body: body, create: true})
}

func runPrincipalUpdate(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The principal ID is invalid."))
	}
	body, members, err := readPrincipalInput(command, options, false)
	if err != nil || len(members) == 0 {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The principal update input is invalid."))
	}
	if members["state"] {
		if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Change this principal's authority state? Disabling immediately clears credential authority and sessions; re-enabling restores neither credentials nor deleted grants."}); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
	}
	etag, failure := resolveMutationETag(command, options, onlineItemPrincipal, args[0])
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	return runPrincipalMutation(command, options, principalMutationRequest{method: http.MethodPatch, path: "/api/v1/principals/" + args[0], body: body, etag: etag, principalID: args[0]})
}

func readPrincipalInput(command *cobra.Command, options *onlineOptions, create bool) ([]byte, map[string]bool, error) {
	allowed := []string{"display_name", "visibility"}
	if !create {
		allowed = append(allowed, "state")
	}
	body, err := readOnlineJSONInput(command, options, allowed)
	if err != nil {
		return nil, nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || len(object) == 0 {
		return nil, nil, controlclient.ErrInvalidInput
	}
	members := make(map[string]bool, len(object))
	for key := range object {
		members[key] = true
	}
	var displayName string
	if raw, ok := object["display_name"]; ok && (json.Unmarshal(raw, &displayName) != nil || len(displayName) == 0 || len(displayName) > 256 || !utf8.ValidString(displayName) || containsControl(displayName)) {
		return nil, nil, controlclient.ErrInvalidInput
	}
	var visibility contract.PrincipalVisibility
	if raw, ok := object["visibility"]; ok {
		if json.Unmarshal(raw, &visibility) != nil {
			return nil, nil, controlclient.ErrInvalidInput
		}
		if _, err := contract.ParsePrincipalVisibility(string(visibility)); err != nil {
			return nil, nil, controlclient.ErrInvalidInput
		}
	}
	var state contract.PrincipalState
	if raw, ok := object["state"]; ok {
		if json.Unmarshal(raw, &state) != nil {
			return nil, nil, controlclient.ErrInvalidInput
		}
		if _, err := contract.ParsePrincipalState(string(state)); err != nil {
			return nil, nil, controlclient.ErrInvalidInput
		}
	}
	canonical, err := json.Marshal(object)
	return canonical, members, err
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

type principalMutationRequest struct {
	method      string
	path        string
	body        []byte
	etag        string
	principalID string
	create      bool
}

func runPrincipalMutation(command *cobra.Command, options *onlineOptions, request principalMutationRequest) error {
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
	response, err := client.Do(command.Context(), controlclient.Request{Method: request.method, Path: request.path, Header: header, Body: request.body})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = principalUncertainTitle(request)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	expectedStatus := http.StatusOK
	if request.create {
		expectedStatus = http.StatusCreated
	}
	if response.StatusCode != expectedStatus {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalUncertainTitle(request), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || len(response.Body) == 0 {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	var principal contract.Principal
	if request.create {
		var creation contract.PrincipalCreation
		if controlclient.DecodeResponse(response.Body, &creation) != nil || creation.DefaultGrant.PrincipalID != creation.Principal.ID {
			return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalUncertainTitle(request), Exit: 8, Uncertain: true})
		}
		principal = creation.Principal
	} else if controlclient.DecodeResponse(response.Body, &principal) != nil {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	if !validPrincipal(principal) || (!request.create && principal.ID != request.principalID) || response.Header.Get("ETag") != contract.PrincipalETag(principal.ID, principal.Revision) || (request.create && response.Header.Get("Location") != "") {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	if mode == controlclient.OutputJSON {
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, principalTable([]contract.Principal{principal}))
}

func principalUncertainTitle(request principalMutationRequest) string {
	if request.create {
		return "The principal create outcome is uncertain. Nothing was replayed. Inspect principal list; reads cannot prove rollback or safely recover an unknown new intent."
	}
	return "The principal update outcome is uncertain. Nothing was replayed or overwritten. Inspect principal get " + request.principalID + " before deciding on a new explicit ETag and input."
}

func validPrincipal(principal contract.Principal) bool {
	if !gatewayIDPattern.MatchString(principal.ID) || !validCanonicalRevision(principal.Revision) || !validCanonicalRevision(principal.CredentialRevision) || principal.DisplayName == "" {
		return false
	}
	_, stateErr := contract.ParsePrincipalState(string(principal.State))
	_, visibilityErr := contract.ParsePrincipalVisibility(string(principal.Visibility))
	return stateErr == nil && visibilityErr == nil
}

func principalListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.Principal]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return withNextCursor(principalTable(page.Items), page.NextCursor), nil
}

func principalItemTable(body []byte) (controlclient.Table, error) {
	var principal contract.Principal
	if err := controlclient.DecodeResponse(body, &principal); err != nil {
		return controlclient.Table{}, err
	}
	return principalTable([]contract.Principal{principal}), nil
}

func principalTable(principals []contract.Principal) controlclient.Table {
	rows := make([][]string, 0, len(principals))
	for _, principal := range principals {
		credential := "absent"
		if principal.Credential != nil {
			credential = principal.Credential.ID + " revision=" + principal.Credential.Revision
		}
		rows = append(rows, []string{principal.ID, principal.DisplayName, string(principal.State), string(principal.Visibility), principal.Revision, principal.CredentialRevision, credential, principal.UpdatedAt})
	}
	return controlclient.Table{Headers: []string{"ID", "DISPLAY", "STATE", "VISIBILITY", "REVISION", "CREDENTIAL_REVISION", "CREDENTIAL", "UPDATED"}, Rows: rows}
}
