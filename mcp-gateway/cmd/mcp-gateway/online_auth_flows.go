package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func authFlowListPath(options *onlineOptions, args []string) (string, error) {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return "", controlclient.ErrInvalidInput
	}
	return controlclient.BuildListPath("/api/v1/servers/"+args[0]+"/auth-flows", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
}

func runServerAuthFlowStart(command *cobra.Command, options *onlineOptions, args []string) error {
	return runServerAuthFlowStartWithTerminal(command, options, args, nil)
}

func runServerAuthFlowStartWithTerminal(command *cobra.Command, options *onlineOptions, args []string, openTerminal func() (io.WriteCloser, error)) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ID is invalid."))
	}
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputHuman), controlclient.NewInputError("The output mode is invalid."))
	}
	sink, err := controlclient.PrepareSensitiveSink(controlclient.SinkOptions{OpenTerminal: openTerminal})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	defer func() { _ = sink.Cleanup() }()
	etag, failure := resolveMutationETag(command, options, onlineItemServer, args[0])
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, ETag: etag})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	sink.MarkSubmitted()
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/servers/" + args[0] + "/auth-flows", Header: header, Body: []byte("{}")})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = authFlowStartUncertainTitle(args[0])
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusCreated {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: authFlowStartUncertainTitle(args[0]), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || len(response.Body) == 0 {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: authFlowStartUncertainTitle(args[0]), Exit: 8, Uncertain: true})
	}
	var creation contract.AuthFlowCreation
	if controlclient.DecodeResponse(response.Body, &creation) != nil || !validAuthFlow(creation.Flow, args[0]) || creation.Flow.FlowState != contract.AuthFlowAwaitingCallback || !validAuthorizationURL(creation.AuthorizationURL) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: authFlowStartUncertainTitle(args[0]), Exit: 8, Uncertain: true})
	}
	if err := sink.Publish(creation.AuthorizationURL); err != nil {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_secret_sink_unavailable", Title: "The auth flow was created, but its one-time authorization URL could not be published. Inspect auth-flow reads and start a new flow after review; the URL cannot be recovered.", Exit: 2})
	}
	if options.open {
		if err := openAuthorizationURL(command.Context(), creation.AuthorizationURL); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The authorization URL was published but could not be opened safely."))
		}
	}
	if mode == controlclient.OutputJSON {
		body, err := json.Marshal(creation.Flow)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The auth-flow result could not be encoded safely."))
		}
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, body, controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, authFlowTable([]contract.ServerAuthFlow{creation.Flow}))
}

func runServerAuthFlowCancel(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 2 || !gatewayIDPattern.MatchString(args[0]) || !gatewayIDPattern.MatchString(args[1]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server or auth-flow ID is invalid."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	readHeader, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value})
	path := "/api/v1/servers/" + args[0] + "/auth-flows/" + args[1]
	readResponse, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodGet, Path: path, Header: readHeader})
	if err != nil {
		return writeOnlineFailure(command, options.output, classifyReadFailure(err))
	}
	if failure := evaluateOnlineResponse(readResponse, options.adminBearer.path); failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	var flow contract.ServerAuthFlow
	if controlclient.DecodeResponse(readResponse.Body, &flow) != nil || !validAuthFlow(flow, args[0]) || flow.ID != args[1] {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_response_invalid", Title: "The Gateway response was invalid.", Exit: 10})
	}
	if flow.FlowState != contract.AuthFlowPreparing && flow.FlowState != contract.AuthFlowAwaitingCallback {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("Only a preparing or awaiting-callback auth flow can be cancelled."))
	}
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Cancel this auth flow and invalidate its callback state and any already-open authorization page? An exchange already in progress cannot be cancelled."}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	deleteHeader, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true})
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodDelete, Path: path, Header: deleteHeader, Body: []byte("{}")})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = authFlowCancelUncertainTitle(args[0], args[1])
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: authFlowCancelUncertainTitle(args[0], args[1]), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if options.output == string(controlclient.OutputJSON) {
		return controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputJSON, []byte("{}"), controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputTable, nil, controlclient.Table{Headers: []string{"FLOW", "RESULT"}, Rows: [][]string{{args[1], "cancelled"}}})
}

func validAuthFlow(flow contract.ServerAuthFlow, serverID string) bool {
	if !gatewayIDPattern.MatchString(flow.ID) || flow.ServerID != serverID || !validCanonicalRevision(flow.TargetDesiredRevision) || !validCanonicalRevision(flow.RegistrationRevision) {
		return false
	}
	_, err := contract.ParseAuthFlowState(string(flow.FlowState))
	return err == nil
}

func validAuthorizationURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 16*1024 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Host == "" || parsed.String() != raw {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && net.ParseIP(parsed.Hostname()) != nil && net.ParseIP(parsed.Hostname()).IsLoopback()
}

func openAuthorizationURL(ctx context.Context, raw string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", raw) //nolint:gosec // The strict URL is passed as one argument without a shell.
	case "linux":
		command = exec.CommandContext(ctx, "xdg-open", raw) //nolint:gosec // The strict URL is passed as one argument without a shell.
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", raw) //nolint:gosec // The strict URL is passed as one argument without a shell.
	default:
		return controlclient.ErrInvalidInput
	}
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func authFlowStartUncertainTitle(serverID string) string {
	return "The auth-flow start outcome is uncertain. Nothing was retried. Inspect server auth-flow list " + serverID + "; reads may reveal a flow but can never recover its one-time authorization URL. Start a new flow only after explicit review."
}

func authFlowCancelUncertainTitle(serverID, flowID string) string {
	return "The auth-flow cancellation outcome is uncertain. Nothing was replayed. Inspect server auth-flow get " + serverID + " " + flowID + "; only a cancelled state proves cancellation, and an eligible state requires a new confirmed attempt."
}

func authFlowListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.ServerAuthFlow]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return authFlowTable(page.Items), nil
}

func authFlowItemTable(body []byte) (controlclient.Table, error) {
	var flow contract.ServerAuthFlow
	if err := controlclient.DecodeResponse(body, &flow); err != nil {
		return controlclient.Table{}, err
	}
	return authFlowTable([]contract.ServerAuthFlow{flow}), nil
}

func authFlowTable(flows []contract.ServerAuthFlow) controlclient.Table {
	rows := make([][]string, 0, len(flows))
	for _, flow := range flows {
		reason := "-"
		if flow.Reason != nil {
			reason = string(*flow.Reason)
		}
		rows = append(rows, []string{flow.ID, flow.ServerID, string(flow.FlowState), flow.TargetDesiredRevision, flow.RegistrationRevision, reason, flow.CreatedAt, flow.ExpiresAt, pointerText(flow.FinishedAt)})
	}
	return controlclient.Table{Headers: []string{"ID", "SERVER", "STATE", "DESIRED_REVISION", "REGISTRATION_REVISION", "REASON", "CREATED", "EXPIRES", "FINISHED"}, Rows: rows}
}
