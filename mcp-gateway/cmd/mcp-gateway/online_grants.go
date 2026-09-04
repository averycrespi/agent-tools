package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"regexp/syntax"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var grantETagPattern = regexp.MustCompile(`^"grant-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)

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
	allowed := []string{"description", "principal_id", "effect", "server_id", "upstream_name", "constraint", "expires_at"}
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
	if string(object["description"]) != "null" {
		var description string
		if json.Unmarshal(object["description"], &description) != nil || !validGrantDescription(description) {
			return nil, controlclient.ErrInvalidInput
		}
	}
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
	return marshalGrantJSON(object)
}

func marshalGrantJSON(value any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte("\n")), nil
}

type grantConstraintShape struct {
	version     int
	equalities  int
	expressions int
}

func validGrantConstraint(raw json.RawMessage) bool {
	_, ok := parseGrantConstraint(raw)
	return ok
}

func parseGrantConstraint(raw json.RawMessage) (grantConstraintShape, bool) {
	limit, _ := contract.FixedLimitByName("constraint_bytes")
	if !limit.Allows(int64(len(raw))) {
		return grantConstraintShape{}, false
	}
	var outer map[string]json.RawMessage
	if json.Unmarshal(raw, &outer) != nil {
		return grantConstraintShape{}, false
	}
	shape := grantConstraintShape{version: 1}
	if len(outer) == 1 && outer["equals"] != nil {
		shape.equalities = validGrantEquals(outer["equals"])
	} else {
		shape.version = 2
		if len(outer) < 2 || len(outer) > 3 || string(outer["version"]) != "2" || (outer["equals"] == nil && outer["regex"] == nil) {
			return grantConstraintShape{}, false
		}
		for member := range outer {
			if member != "version" && member != "equals" && member != "regex" {
				return grantConstraintShape{}, false
			}
		}
		if outer["equals"] != nil {
			shape.equalities = validGrantEquals(outer["equals"])
		}
		if outer["regex"] != nil {
			shape.expressions = validGrantRegex(outer["regex"])
		}
	}
	atomLimit, _ := contract.FixedLimitByName("constraint_atoms")
	atoms := shape.equalities + shape.expressions
	if shape.equalities < 0 || shape.expressions < 0 || atoms < 1 || !atomLimit.Allows(int64(atoms)) {
		return grantConstraintShape{}, false
	}
	return shape, true
}

func validGrantEquals(raw json.RawMessage) int {
	var equals map[string]json.RawMessage
	if json.Unmarshal(raw, &equals) != nil || equals == nil {
		return -1
	}
	for pointer, scalar := range equals {
		if !validGrantPointer(pointer) || !validJSONScalar(scalar) {
			return -1
		}
	}
	return len(equals)
}

func validGrantRegex(raw json.RawMessage) int {
	var expressions map[string]json.RawMessage
	if json.Unmarshal(raw, &expressions) != nil || expressions == nil {
		return -1
	}
	patternLimit, _ := contract.FixedLimitByName("constraint_regex_pattern_bytes")
	programLimit, _ := contract.FixedLimitByName("constraint_regex_program_instructions")
	for pointer, rawPattern := range expressions {
		var pattern string
		if !validGrantPointer(pointer) || json.Unmarshal(rawPattern, &pattern) != nil || !patternLimit.Allows(int64(len(pattern))) {
			return -1
		}
		if _, err := syntax.Parse(pattern, syntax.Perl); err != nil {
			return -1
		}
		parsed, err := syntax.Parse(`\A(?:`+pattern+`)\z`, syntax.Perl)
		if err != nil {
			return -1
		}
		program, err := syntax.Compile(parsed.Simplify())
		if err != nil || !programLimit.Allows(int64(len(program.Inst))) {
			return -1
		}
	}
	return len(expressions)
}

func validGrantPointer(pointer string) bool {
	limit, _ := contract.FixedLimitByName("constraint_pointer_bytes")
	if !strings.HasPrefix(pointer, "/") || !limit.Allows(int64(len(pointer))) || !utf8.ValidString(pointer) {
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

func grantConstraintSummary(raw *json.RawMessage) string {
	if raw == nil {
		return "none"
	}
	shape, ok := parseGrantConstraint(*raw)
	if !ok {
		return "invalid"
	}
	if shape.version == 1 {
		return fmt.Sprintf("v1 equals (%d)", shape.equalities)
	}
	return fmt.Sprintf("v2 equals (%d), regex (%d)", shape.equalities, shape.expressions)
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
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, grantTable([]contract.Grant{grant}, false))
}

func runGrantUpdate(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) || options.intent.body == nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant update input is invalid."))
	}
	var input struct {
		Description *string `json:"description"`
	}
	if controlclient.DecodeResponse(options.intent.body, &input) != nil || (input.Description != nil && !validGrantDescription(*input.Description)) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant update input is invalid."))
	}
	etag, failure := resolveMutationETag(command, options, onlineItemGrant, args[0])
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, ETag: etag})
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPatch, Path: "/api/v1/grants/" + args[0], Header: header, Body: options.intent.body})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	if failure := evaluateOnlineResponse(response, options.adminBearer.path); failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != contract.MediaTypeJSON {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(controlclient.ErrResponseInvalid))
	}
	var grant contract.Grant
	if controlclient.DecodeResponse(response.Body, &grant) != nil || !validGrant(grant) || !contract.MatchesGrantETag(response.Header.Get("ETag"), grant.ID, grant.Revision) {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(controlclient.ErrResponseInvalid))
	}
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	if mode == controlclient.OutputJSON {
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, grantTable([]contract.Grant{grant}, false))
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
	if !gatewayIDPattern.MatchString(grant.ID) || (grant.Description != nil && !validGrantDescription(*grant.Description)) || grant.Revision == "" || !gatewayIDPattern.MatchString(grant.PrincipalID) || !gatewayIDPattern.MatchString(grant.ServerID) {
		return false
	}
	_, effectErr := contract.ParseGrantEffect(string(grant.Effect))
	_, stateErr := contract.ParseGrantState(string(grant.State))
	return effectErr == nil && stateErr == nil && (grant.UpstreamName != nil || grant.Constraint == nil) && grantConstraintSummary(grant.Constraint) != "invalid"
}

func validGrantDescription(value string) bool {
	return len(value) >= 1 && len(value) <= 256 && utf8.ValidString(value) && !containsControl(value) && strings.TrimSpace(value) == value
}

func grantListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.Grant]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return withNextCursor(grantTable(page.Items, true), page.NextCursor), nil
}

func grantItemTable(body []byte) (controlclient.Table, error) {
	var grant contract.Grant
	if err := controlclient.DecodeResponse(body, &grant); err != nil {
		return controlclient.Table{}, err
	}
	return grantTable([]contract.Grant{grant}, false), nil
}

func grantTable(grants []contract.Grant, truncateDescriptions bool) controlclient.Table {
	rows := make([][]string, 0, len(grants))
	for _, grant := range grants {
		upstream := "all tools"
		if grant.UpstreamName != nil {
			upstream = *grant.UpstreamName
		}
		constraint := grantConstraintSummary(grant.Constraint)
		description := "—"
		if grant.Description != nil {
			description = *grant.Description
			characters := []rune(description)
			if truncateDescriptions && len(characters) > 64 {
				description = string(characters[:61]) + "…"
			}
		}
		rows = append(rows, []string{description, grant.ID, grant.PrincipalID, string(grant.Effect), grant.ServerID, upstream, constraint, pointerText(grant.ExpiresAt), string(grant.State), grant.CreatedAt})
	}
	return controlclient.Table{Headers: []string{"DESCRIPTION", "ID", "PRINCIPAL", "EFFECT", "SERVER", "UPSTREAM", "CONSTRAINT", "EXPIRES", "STATE", "CREATED"}, Rows: rows}
}
