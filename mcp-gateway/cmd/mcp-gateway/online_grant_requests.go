package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var grantRequestETagPattern = regexp.MustCompile(`^"grant-request-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)

func grantRequestListPath(options *onlineOptions) (string, error) {
	filters := map[string]string{}
	if value := options.filters["principal-id"]; value != nil && *value != "" {
		if !gatewayIDPattern.MatchString(*value) {
			return "", controlclient.ErrInvalidInput
		}
		filters["principal_id"] = *value
	}
	if value := options.filters["state"]; value != nil && *value != "" {
		if _, err := contract.ParseGrantRequestState(*value); err != nil {
			return "", controlclient.ErrInvalidInput
		}
		filters["state"] = *value
	}
	return controlclient.BuildListPath("/api/v1/grant-requests", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor, Filters: filters, AllowedFilters: []string{"principal_id", "state"}})
}

func runGrantRequestApprove(command *cobra.Command, options *onlineOptions, args []string) error {
	requestID, ok := validGrantRequestArgs(options, args)
	if !ok {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant-request ID or ETag is invalid."))
	}
	body, policy, err := readGrantRequestApproval(command, options)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant-request approval input is invalid."))
	}
	consequence := "Approve this request and atomically create one immutable ALLOW grant? This closes the request but executes, resumes, and replays no motivating call."
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: consequence}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	return runGrantRequestAdjudication(command, options, grantRequestAdjudication{requestID: requestID, action: "approve", body: body, approvedPolicy: &policy})
}

func runGrantRequestReject(command *cobra.Command, options *onlineOptions, args []string) error {
	requestID, ok := validGrantRequestArgs(options, args)
	if !ok {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant-request ID or ETag is invalid."))
	}
	body, reason, err := readGrantRequestRejection(command, options)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant-request rejection input is invalid."))
	}
	consequence := "Reject this request with reason " + string(reason) + " and create no grant? This terminal action executes, resumes, and replays no motivating call."
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: consequence}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	return runGrantRequestAdjudication(command, options, grantRequestAdjudication{requestID: requestID, action: "reject", body: body, rejectionReason: &reason})
}

func validGrantRequestArgs(options *onlineOptions, args []string) (string, bool) {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return "", false
	}
	parts := grantRequestETagPattern.FindStringSubmatch(options.etag)
	return args[0], len(parts) == 3 && parts[1] == args[0]
}

func readGrantRequestApproval(command *cobra.Command, options *onlineOptions) ([]byte, contract.Policy, error) {
	body, err := controlclient.ReadJSONInput(controlclient.InputOptions{Path: options.file, Stdin: command.InOrStdin(), AllowedMembers: []string{"approved_policy"}})
	if err != nil {
		return nil, contract.Policy{}, err
	}
	var outer map[string]json.RawMessage
	if json.Unmarshal(body, &outer) != nil || len(outer) != 1 || outer["approved_policy"] == nil {
		return nil, contract.Policy{}, controlclient.ErrInvalidInput
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(outer["approved_policy"], &raw) != nil || len(raw) != 5 {
		return nil, contract.Policy{}, controlclient.ErrInvalidInput
	}
	for _, member := range []string{"scope", "target", "constraint", "duration_seconds", "future_tools_acknowledged"} {
		if raw[member] == nil {
			return nil, contract.Policy{}, controlclient.ErrInvalidInput
		}
	}
	var policy contract.Policy
	if json.Unmarshal(raw["scope"], &policy.Scope) != nil || json.Unmarshal(raw["target"], &policy.Target) != nil || json.Unmarshal(raw["future_tools_acknowledged"], &policy.FutureToolsAcknowledged) != nil || policy.Target == "" || len(policy.Target) > 256 || !utf8.ValidString(policy.Target) || containsControl(policy.Target) {
		return nil, contract.Policy{}, controlclient.ErrInvalidInput
	}
	if _, err := contract.ParsePolicyScope(string(policy.Scope)); err != nil {
		return nil, contract.Policy{}, controlclient.ErrInvalidInput
	}
	if string(raw["constraint"]) != "null" {
		if !validGrantConstraint(raw["constraint"]) {
			return nil, contract.Policy{}, controlclient.ErrInvalidInput
		}
		value := json.RawMessage(append([]byte(nil), raw["constraint"]...))
		policy.Constraint = &value
	}
	if string(raw["duration_seconds"]) != "null" {
		var value string
		if json.Unmarshal(raw["duration_seconds"], &value) != nil || !canonicalRevisionPattern.MatchString(value) {
			return nil, contract.Policy{}, controlclient.ErrInvalidInput
		}
		duration, err := strconv.ParseInt(value, 10, 64)
		if err != nil || duration < 60 || duration > 2592000 {
			return nil, contract.Policy{}, controlclient.ErrInvalidInput
		}
		policy.DurationSeconds = &value
	}
	if (policy.Scope == contract.PolicyTool && policy.FutureToolsAcknowledged) || (policy.Scope == contract.PolicyServer && (!policy.FutureToolsAcknowledged || policy.Constraint != nil)) {
		return nil, contract.Policy{}, controlclient.ErrInvalidInput
	}
	canonical, err := json.Marshal(contract.GrantRequestApproval{ApprovedPolicy: policy})
	return canonical, policy, err
}

func readGrantRequestRejection(command *cobra.Command, options *onlineOptions) ([]byte, contract.GrantRequestRejectionReason, error) {
	body, err := controlclient.ReadJSONInput(controlclient.InputOptions{Path: options.file, Stdin: command.InOrStdin(), AllowedMembers: []string{"reason"}})
	if err != nil {
		return nil, "", err
	}
	var raw map[string]json.RawMessage
	var reason contract.GrantRequestRejectionReason
	if json.Unmarshal(body, &raw) != nil || len(raw) != 1 || json.Unmarshal(raw["reason"], &reason) != nil {
		return nil, "", controlclient.ErrInvalidInput
	}
	if _, err := contract.ParseGrantRequestRejectionReason(string(reason)); err != nil {
		return nil, "", controlclient.ErrInvalidInput
	}
	canonical, err := json.Marshal(contract.GrantRequestRejection{Reason: reason})
	return canonical, reason, err
}

type grantRequestAdjudication struct {
	requestID       string
	action          string
	body            []byte
	approvedPolicy  *contract.Policy
	rejectionReason *contract.GrantRequestRejectionReason
}

func runGrantRequestAdjudication(command *cobra.Command, options *onlineOptions, adjudication grantRequestAdjudication) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, ETag: options.etag})
	path := "/api/v1/grant-requests/" + adjudication.requestID + "/" + adjudication.action
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: path, Header: header, Body: adjudication.body})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = grantRequestUncertainTitle(adjudication.requestID)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusOK {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || response.StatusCode == http.StatusServiceUnavailable || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: grantRequestUncertainTitle(adjudication.requestID), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	var request contract.GrantRequest
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || controlclient.DecodeResponse(response.Body, &request) != nil || !validGrantRequest(request) || request.ID != adjudication.requestID || response.Header.Get("ETag") != contract.GrantRequestETag(request.ID, request.Revision) || !validAdjudicationResult(request, adjudication) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: grantRequestUncertainTitle(adjudication.requestID), Exit: 8, Uncertain: true})
	}
	if mode == controlclient.OutputJSON {
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, grantRequestTable([]contract.GrantRequestSummary{request.GrantRequestSummary}, &request.CurrentTarget))
}

func validAdjudicationResult(request contract.GrantRequest, adjudication grantRequestAdjudication) bool {
	if request.Revision != "2" {
		return false
	}
	if adjudication.action == "approve" {
		return request.State == contract.RequestApproved && request.ApprovedGrantID != nil && gatewayIDPattern.MatchString(*request.ApprovedGrantID) && request.ApprovedPolicy != nil && adjudication.approvedPolicy != nil && reflect.DeepEqual(*request.ApprovedPolicy, *adjudication.approvedPolicy) && request.RejectionReason == nil
	}
	return request.State == contract.RequestRejected && request.RejectionReason != nil && adjudication.rejectionReason != nil && *request.RejectionReason == *adjudication.rejectionReason && request.ApprovedGrantID == nil && request.ApprovedPolicy == nil
}

func grantRequestUncertainTitle(id string) string {
	return "The grant-request adjudication outcome is uncertain. Nothing was replayed. Inspect grant-request get " + id + " and relevant grant reads; request state or a grant ID is not proof that a motivating call executed or resumed."
}

func validGrantRequest(request contract.GrantRequest) bool {
	if !gatewayIDPattern.MatchString(request.ID) || !gatewayIDPattern.MatchString(request.PrincipalID) || !gatewayIDPattern.MatchString(request.ResolvedServerID) || !validCanonicalRevision(request.Revision) {
		return false
	}
	_, err := contract.ParseGrantRequestState(string(request.State))
	return err == nil
}

func grantRequestListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.GrantRequestSummary]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return grantRequestTable(page.Items, nil), nil
}

func grantRequestItemTable(body []byte) (controlclient.Table, error) {
	var request contract.GrantRequest
	if err := controlclient.DecodeResponse(body, &request); err != nil {
		return controlclient.Table{}, err
	}
	return grantRequestTable([]contract.GrantRequestSummary{request.GrantRequestSummary}, &request.CurrentTarget), nil
}

func grantRequestTable(requests []contract.GrantRequestSummary, target *contract.TargetComparison) controlclient.Table {
	rows := make([][]string, 0, len(requests))
	for _, request := range requests {
		approvedGrant := pointerText(request.ApprovedGrantID)
		reason := "-"
		if request.RejectionReason != nil {
			reason = string(*request.RejectionReason)
		}
		targetState := "summary only"
		if target != nil {
			targetState = string(target.TargetState)
			if target.DurableState != nil {
				targetState += "/" + string(*target.DurableState)
			}
		}
		rows = append(rows, []string{request.ID, request.PrincipalID, string(request.State), request.Revision, string(request.RequestedPolicy.Scope), request.RequestedPolicy.Target, approvedGrant, reason, targetState, request.UpdatedAt})
	}
	return controlclient.Table{Headers: []string{"ID", "PRINCIPAL", "STATE", "REVISION", "SCOPE", "TARGET", "GRANT", "REASON", "CURRENT_TARGET", "UPDATED"}, Rows: rows}
}
