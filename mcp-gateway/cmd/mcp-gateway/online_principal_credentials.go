package main

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var agentBearerPattern = regexp.MustCompile(`^mgw_agent_[A-Za-z0-9_-]{43}$`)

func runPrincipalCredentialIssue(command *cobra.Command, options *onlineOptions, args []string) error {
	return runPrincipalCredentialMutation(command, options, args, false)
}

func runPrincipalCredentialRotate(command *cobra.Command, options *onlineOptions, args []string) error {
	return runPrincipalCredentialMutation(command, options, args, true)
}

func runPrincipalCredentialMutation(command *cobra.Command, options *onlineOptions, args []string, rotate bool) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The principal ID is invalid."))
	}
	principalID := args[0]
	action := "issue"
	consequence := "Issue this principal's first singular bearer?"
	if rotate {
		action = "rotate"
		consequence = "Atomically replace this principal's singular bearer with no authority overlap? Current bearer authority, sessions, streams, and admitted leases will be interrupted."
	}
	sink, failure := prepareOnlineSensitiveAction(options, consequence, nil, nil)
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	defer func() { _ = sink.Cleanup() }()
	item, failure := loadValidatedItem(command, options, onlineItemPrincipal, principalID, controlclient.RequestPhasePreflight)
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	if options.etag != "" && options.etag != item.ETag {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The explicit principal ETag does not match the loaded principal."))
	}
	var current contract.Principal
	if controlclient.DecodeResponse(item.Body, &current) != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyRequestError(controlclient.ErrResponseInvalid, controlclient.RequestPhasePreflight))
	}
	if (!rotate && current.Credential != nil) || (rotate && current.Credential == nil) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("Principal credential "+action+" does not match the current credential slot state."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, ETag: item.ETag})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	sink.MarkSubmitted()
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/principals/" + principalID + "/credential", Header: header, Body: []byte("{}")})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = principalCredentialMutationUncertainTitle(action, principalID)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusCreated {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalCredentialMutationUncertainTitle(action, principalID), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || len(response.Body) == 0 {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalCredentialMutationUncertainTitle(action, principalID), Exit: 8, Uncertain: true})
	}
	var creation contract.AgentCredentialCreation
	if controlclient.DecodeResponse(response.Body, &creation) != nil || !validPrincipal(creation.Principal) || creation.Principal.ID != principalID || creation.Principal.Credential == nil || !agentBearerPattern.MatchString(creation.Bearer) || response.Header.Get("ETag") != contract.PrincipalETag(principalID, creation.Principal.Revision) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalCredentialMutationUncertainTitle(action, principalID), Exit: 8, Uncertain: true})
	}
	if err := sink.Publish(creation.Bearer); err != nil {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_secret_sink_unavailable", Title: "The principal credential was " + action + "d, but its one-time bearer could not be published. It cannot be recovered; inspect principal metadata before deciding whether to revoke it.", Exit: 2})
	}
	return writePrincipalMetadataSuccess(command, options, creation.Principal, true)
}

func runPrincipalCredentialRevoke(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The principal ID is invalid."))
	}
	principalID := args[0]
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Revoke this principal's singular bearer and close its sessions, streams, and admitted leases? No bearer will remain."}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	etag, failure := resolveMutationETag(command, options, onlineItemPrincipal, principalID)
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, ETag: etag})
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodDelete, Path: "/api/v1/principals/" + principalID + "/credential", Header: header, Body: []byte("{}")})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = principalCredentialRevokeUncertainTitle(principalID)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusOK {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalCredentialRevokeUncertainTitle(principalID), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	var principal contract.Principal
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || controlclient.DecodeResponse(response.Body, &principal) != nil || !validPrincipal(principal) || principal.ID != principalID || principal.Credential != nil || response.Header.Get("ETag") != contract.PrincipalETag(principalID, principal.Revision) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: principalCredentialRevokeUncertainTitle(principalID), Exit: 8, Uncertain: true})
	}
	return writePrincipalMetadataSuccess(command, options, principal, false)
}

func writePrincipalMetadataSuccess(command *cobra.Command, options *onlineOptions, principal contract.Principal, bearerPublished bool) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	if mode == controlclient.OutputJSON {
		body, err := json.Marshal(principal)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command output could not be encoded."))
		}
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, body, controlclient.Table{})
	}
	table := principalTable([]contract.Principal{principal})
	if bearerPublished {
		table.Notes = []string{oneTimeBearerPublicationNote(options.secretOutput)}
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, table)
}

func principalCredentialMutationUncertainTitle(action, id string) string {
	return "The principal credential " + action + " outcome is uncertain. Nothing was replayed. Inspect principal get " + id + "; metadata may show a new credential but cannot recover its bearer or prove publication."
}

func principalCredentialRevokeUncertainTitle(id string) string {
	return "The principal credential revoke outcome is uncertain. Nothing was replayed. Inspect principal get " + id + "; only current metadata can guide a new explicit ETag action."
}
