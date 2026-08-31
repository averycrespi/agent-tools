package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func grantListPath(options *onlineOptions) (string, error) {
	filters := map[string]string{}
	for cliName, apiName := range map[string]string{"principal-id": "principal_id", "server-id": "server_id"} {
		if value := options.filters[cliName]; value != nil && *value != "" {
			if !gatewayIDPattern.MatchString(*value) {
				return "", controlclient.ErrInvalidInput
			}
			filters[apiName] = *value
		}
	}
	return controlclient.BuildListPath("/api/v1/grants", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor, Filters: filters, AllowedFilters: []string{"principal_id", "server_id"}})
}

func runGrantCreate(command *cobra.Command, options *onlineOptions) error {
	body, err := readGrantCreateInput(command, options)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant create input is invalid."))
	}
	return runGrantCreateRequest(command, options, body)
}

func readGrantCreateInput(command *cobra.Command, options *onlineOptions) ([]byte, error) {
	allowed := []string{"principal_id", "effect", "server_id", "upstream_name", "constraint", "expires_at"}
	body, err := readOnlineJSONInput(command, options, allowed)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || len(object) != len(allowed) {
		return nil, controlclient.ErrInvalidInput
	}
	for _, member := range allowed {
		if object[member] == nil {
			return nil, controlclient.ErrInvalidInput
		}
	}
	var principalID, serverID string
	var effect contract.GrantEffect
	if json.Unmarshal(object["principal_id"], &principalID) != nil || !gatewayIDPattern.MatchString(principalID) || json.Unmarshal(object["server_id"], &serverID) != nil || !gatewayIDPattern.MatchString(serverID) || json.Unmarshal(object["effect"], &effect) != nil {
		return nil, controlclient.ErrInvalidInput
	}
	if _, err := contract.ParseGrantEffect(string(effect)); err != nil {
		return nil, controlclient.ErrInvalidInput
	}
	var upstream *string
	if string(object["upstream_name"]) != "null" {
		var value string
		if json.Unmarshal(object["upstream_name"], &value) != nil || value == "" || len(value) > 256 || !utf8.ValidString(value) || containsControl(value) {
			return nil, controlclient.ErrInvalidInput
		}
		upstream = &value
	}
	constraintNull := string(object["constraint"]) == "null"
	if (upstream == nil && !constraintNull) || (!constraintNull && !validGrantConstraint(object["constraint"])) {
		return nil, controlclient.ErrInvalidInput
	}
	if string(object["expires_at"]) != "null" {
		var value string
		if json.Unmarshal(object["expires_at"], &value) != nil || !strings.HasSuffix(value, "Z") {
			return nil, controlclient.ErrInvalidInput
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || expiresAt.Format(time.RFC3339Nano) != value || !expiresAt.After(time.Now()) {
			return nil, controlclient.ErrInvalidInput
		}
	}
	return json.Marshal(object)
}

func validGrantConstraint(raw json.RawMessage) bool {
	if len(raw) > 8192 {
		return false
	}
	var outer map[string]json.RawMessage
	if json.Unmarshal(raw, &outer) != nil || len(outer) != 1 || outer["equals"] == nil {
		return false
	}
	var equals map[string]json.RawMessage
	if json.Unmarshal(outer["equals"], &equals) != nil || len(equals) < 1 || len(equals) > 16 {
		return false
	}
	for pointer, scalar := range equals {
		if !validGrantPointer(pointer) || !validJSONScalar(scalar) {
			return false
		}
	}
	return true
}

func validGrantPointer(pointer string) bool {
	if !strings.HasPrefix(pointer, "/") || len(pointer) > 256 || !utf8.ValidString(pointer) {
		return false
	}
	for index := 0; index < len(pointer); index++ {
		if pointer[index] == '~' {
			if index+1 >= len(pointer) || (pointer[index+1] != '0' && pointer[index+1] != '1') {
				return false
			}
			index++
		}
	}
	return true
}

func validJSONScalar(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] == '{' || trimmed[0] == '[' {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	switch value.(type) {
	case nil, bool, string, json.Number:
		return true
	default:
		return false
	}
}

func runGrantCreateRequest(command *cobra.Command, options *onlineOptions, body []byte) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true})
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/grants", Header: header, Body: body})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = grantCreateUncertainTitle()
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusCreated {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: grantCreateUncertainTitle(), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	var grant contract.Grant
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || controlclient.DecodeResponse(response.Body, &grant) != nil || !validGrant(grant) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: grantCreateUncertainTitle(), Exit: 8, Uncertain: true})
	}
	if mode == controlclient.OutputJSON {
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, grantTable([]contract.Grant{grant}))
}

func runGrantDelete(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant ID is invalid."))
	}
	consequence := "Delete this immutable grant and release its retained slot? Expired grants still consume capacity. Deleting a default ALLOW removes self-service authority; allowed-only discovery may lose visibility, while visibility alone never authorizes calls."
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: consequence}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value})
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodDelete, Path: "/api/v1/grants/" + args[0], Header: header})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = grantDeleteUncertainTitle(args[0])
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: grantDeleteUncertainTitle(args[0]), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if options.output == string(controlclient.OutputJSON) {
		return controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputJSON, []byte("{}"), controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputTable, nil, controlclient.Table{Headers: []string{"ID", "RESULT"}, Rows: [][]string{{args[0], "deleted"}}})
}

func grantCreateUncertainTitle() string {
	return "The grant create outcome is uncertain. Nothing was replayed. Inspect a narrowly filtered grant list; identical immutable rows mean reads cannot prove rollback or identify an unknown new intent."
}

func grantDeleteUncertainTitle(id string) string {
	return "The grant delete outcome is uncertain. Nothing was replayed. Inspect grant get " + id + " before deciding whether to make a new confirmed attempt."
}

func validGrant(grant contract.Grant) bool {
	if !gatewayIDPattern.MatchString(grant.ID) || !gatewayIDPattern.MatchString(grant.PrincipalID) || !gatewayIDPattern.MatchString(grant.ServerID) {
		return false
	}
	_, effectErr := contract.ParseGrantEffect(string(grant.Effect))
	_, stateErr := contract.ParseGrantState(string(grant.State))
	return effectErr == nil && stateErr == nil
}

func grantListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.Grant]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return withNextCursor(grantTable(page.Items), page.NextCursor), nil
}

func grantItemTable(body []byte) (controlclient.Table, error) {
	var grant contract.Grant
	if err := controlclient.DecodeResponse(body, &grant); err != nil {
		return controlclient.Table{}, err
	}
	return grantTable([]contract.Grant{grant}), nil
}

func grantTable(grants []contract.Grant) controlclient.Table {
	rows := make([][]string, 0, len(grants))
	for _, grant := range grants {
		upstream := "all tools"
		if grant.UpstreamName != nil {
			upstream = *grant.UpstreamName
		}
		constraint := "none"
		if grant.Constraint != nil {
			constraint = "scalar equals"
		}
		rows = append(rows, []string{grant.ID, grant.PrincipalID, string(grant.Effect), grant.ServerID, upstream, constraint, pointerText(grant.ExpiresAt), string(grant.State), grant.CreatedAt})
	}
	return controlclient.Table{Headers: []string{"ID", "PRINCIPAL", "EFFECT", "SERVER", "UPSTREAM", "CONSTRAINT", "EXPIRES", "STATE", "CREATED"}, Rows: rows}
}
