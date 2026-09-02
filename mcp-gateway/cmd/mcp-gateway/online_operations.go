package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func operationListPath(options *onlineOptions, args []string) (string, error) {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return "", controlclient.ErrInvalidInput
	}
	return controlclient.BuildListPath("/api/v1/servers/"+args[0]+"/operations", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
}

func runServerOperationStart(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ID is invalid."))
	}
	body, err := readOnlineJSONInput(command, options, []string{"kind"})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	var input contract.ServerOperationCreate
	if controlclient.DecodeResponse(body, &input) != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The operation input is invalid."))
	}
	kind, err := contract.ParseExplicitServerOperationKind(string(input.Kind))
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The operation kind is invalid."))
	}
	body, err = json.Marshal(input)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The operation input is invalid."))
	}
	if kind == contract.OperationReload || kind == contract.OperationDisconnectCredentials {
		if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{
			Yes: options.yes, Consequence: operationConsequence(kind),
		}); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
	}
	key := options.idempotencyKey
	if key == "" {
		key, err = generateIdempotencyKey()
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("An idempotency key could not be generated."))
		}
	}
	etag, failure := resolveMutationETag(command, options, onlineItemServer, args[0])
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	return runOperationMutation(command, options, operationMutationRequest{serverID: args[0], etag: etag, key: key, body: body, kind: kind})
}

func operationConsequence(kind contract.ServerOperationKind) string {
	if kind == contract.OperationDisconnectCredentials {
		return "Disconnect this server's credentials, invalidate local authority, and attempt best-effort remote revocation?"
	}
	return "Reload this server runtime and interrupt in-flight upstream work?"
}

type operationMutationRequest struct {
	serverID string
	etag     string
	key      string
	body     []byte
	kind     contract.ServerOperationKind
}

func runOperationMutation(command *cobra.Command, options *onlineOptions, request operationMutationRequest) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, ETag: request.etag, IdempotencyKey: request.key})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/servers/" + request.serverID + "/operations", Header: header, Body: request.body})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = operationUncertainTitle(request)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: operationUncertainTitle(request), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || len(response.Body) == 0 {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: operationUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	var mutation contract.ServerOperationMutation
	etagParts := serverETagPattern.FindStringSubmatch(request.etag)
	var stateErr error
	if controlclient.DecodeResponse(response.Body, &mutation) != nil {
		stateErr = controlclient.ErrResponseInvalid
	} else {
		_, stateErr = contract.ParseServerOperationState(string(mutation.Operation.State))
	}
	if stateErr != nil || len(etagParts) != 3 || mutation.Operation.ServerID != request.serverID || mutation.Operation.Kind != request.kind || mutation.Operation.TargetDesiredRevision != etagParts[2] || !gatewayIDPattern.MatchString(mutation.Operation.ID) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: operationUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	if mode == controlclient.OutputJSON {
		if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{}); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return nil
	}
	if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, operationTable([]contract.ServerOperation{mutation.Operation})); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command output could not be written."))
	}
	return nil
}

func operationUncertainTitle(request operationMutationRequest) string {
	digest := sha256.Sum256(request.body)
	return fmt.Sprintf("The server operation outcome is uncertain. Nothing was retried or polled. Inspect server operation list %s before deliberately replaying the same tuple: idempotency key %s, ETag %s, input digest sha256:%s.", request.serverID, request.key, request.etag, hex.EncodeToString(digest[:]))
}

func operationListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.ServerOperation]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return withNextCursor(operationTable(page.Items), page.NextCursor), nil
}

func operationItemTable(body []byte) (controlclient.Table, error) {
	var operation contract.ServerOperation
	if err := controlclient.DecodeResponse(body, &operation); err != nil {
		return controlclient.Table{}, err
	}
	return operationTable([]contract.ServerOperation{operation}), nil
}

func operationTable(operations []contract.ServerOperation) controlclient.Table {
	rows := make([][]string, 0, len(operations))
	for _, operation := range operations {
		reason := "-"
		if operation.Reason != nil {
			reason = string(*operation.Reason)
		}
		rows = append(rows, []string{operation.ID, operation.ServerID, string(operation.Kind), string(operation.State), operation.TargetDesiredRevision, reason, operation.CreatedAt, pointerText(operation.FinishedAt)})
	}
	return controlclient.Table{Headers: []string{"ID", "SERVER", "KIND", "STATE", "REVISION", "REASON", "CREATED", "FINISHED"}, Rows: rows}
}
