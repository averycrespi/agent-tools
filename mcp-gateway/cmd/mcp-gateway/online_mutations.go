package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var serverETagPattern = regexp.MustCompile(`^"server-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)

type serverMutationInputError struct {
	field contract.ServerConfigurationField
	rule  contract.ServerConfigurationRule
}

func (failure *serverMutationInputError) Error() string { return "server mutation input is invalid" }

func invalidServerMutationInput(field contract.ServerConfigurationField, rule contract.ServerConfigurationRule) error {
	return &serverMutationInputError{field: field, rule: rule}
}

type serverMutationWire struct {
	Server    serverWire                `json:"server"`
	Operation *contract.ServerOperation `json:"operation"`
}

func runServerCreate(command *cobra.Command, options *onlineOptions) error {
	body, _, err := readServerMutationInput(command, options, true)
	if err != nil {
		var inputError *serverMutationInputError
		if errors.As(err, &inputError) {
			return writeOnlineFailure(command, options.output, controlclient.NewServerConfigurationInputError(string(inputError.field), string(inputError.rule)))
		}
		return writeOnlineFailure(command, options.output, controlclient.NewServerConfigurationInputError("configuration", "invalid"))
	}
	key := options.idempotencyKey
	if key == "" {
		key, err = generateIdempotencyKey()
	}
	if err != nil || !validIdempotencyKey(key) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The idempotency key is invalid."))
	}
	return runServerMutation(command, options, serverMutationRequest{
		method: http.MethodPost, path: "/api/v1/servers", body: body, idempotencyKey: key,
		successStatuses: map[int]struct{}{http.StatusOK: {}, http.StatusCreated: {}}, create: true,
	})
}

func runServerDelete(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ID is invalid."))
	}
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{
		Yes:         options.yes,
		Consequence: "Permanently delete this server identity, withdraw its routes, invalidate local authority, and schedule cleanup? Remote revocation is best effort and cannot be guaranteed.",
	}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	etag, failure := resolveMutationETag(command, options, onlineItemServer, args[0])
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	return runServerMutation(command, options, serverMutationRequest{
		method: http.MethodDelete, path: "/api/v1/servers/" + args[0], body: []byte("{}"), etag: etag,
		successStatuses: map[int]struct{}{http.StatusOK: {}, http.StatusAccepted: {}}, serverID: args[0], delete: true,
	})
}

func runServerUpdate(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ID is invalid."))
	}
	body, members, err := readServerMutationInput(command, options, false)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("Server update requires a nonempty display_name, enabled, or complete transport patch."))
	}
	if members["enabled"] || members["transport"] {
		if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{
			Yes:         options.yes,
			Consequence: "This behavioral server update may withdraw routes and interrupt affected calls; completed effects can have unknown outcomes. Continue?",
		}); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
	}
	etag, failure := resolveMutationETag(command, options, onlineItemServer, args[0])
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	return runServerMutation(command, options, serverMutationRequest{
		method: http.MethodPatch, path: "/api/v1/servers/" + args[0], body: body, etag: etag,
		successStatuses: map[int]struct{}{http.StatusOK: {}}, serverID: args[0],
	})
}

type serverMutationRequest struct {
	method          string
	path            string
	body            []byte
	etag            string
	idempotencyKey  string
	successStatuses map[int]struct{}
	serverID        string
	create          bool
	delete          bool
}

func runServerMutation(command *cobra.Command, options *onlineOptions, request serverMutationRequest) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{
		Bearer: options.adminBearer.value, ETag: request.etag, IdempotencyKey: request.idempotencyKey, JSONBody: true,
	})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: request.method, Path: request.path, Header: header, Body: request.body})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Uncertain {
			failure.Title = mutationUncertainTitle(request)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if _, ok := request.successStatuses[response.StatusCode]; !ok {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: mutationUncertainTitle(request), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || len(response.Body) == 0 {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: mutationUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	var mutation serverMutationWire
	if controlclient.DecodeResponse(response.Body, &mutation) != nil || !contract.MatchesServerETag(response.Header.Get("ETag"), mutation.Server.ID, mutation.Server.DesiredRevision) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: mutationUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	if request.create {
		if response.Header.Get("Location") != "/api/v1/servers/"+mutation.Server.ID {
			return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: mutationUncertainTitle(request), Exit: 8, Uncertain: true})
		}
	} else if mutation.Server.ID != request.serverID {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: mutationUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	if request.delete && (mutation.Server.DesiredState != contract.DesiredServerDeleted || string(mutation.Server.Transport) != "null" || mutation.Operation == nil || mutation.Operation.ServerID != request.serverID || mutation.Operation.Kind != contract.OperationDelete) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: mutationUncertainTitle(request), Exit: 8, Uncertain: true})
	}
	if mode == controlclient.OutputJSON {
		if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{}); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return nil
	}
	operation := "none"
	if mutation.Operation != nil {
		operation = mutation.Operation.ID + ":" + string(mutation.Operation.State)
	}
	table := controlclient.Table{Headers: append(serverHeaders(), "OPERATION"), Rows: [][]string{append(serverRow(mutation.Server), operation)}}
	if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, table); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command output could not be written."))
	}
	return nil
}

func mutationUncertainTitle(request serverMutationRequest) string {
	digest := sha256.Sum256(request.body)
	if request.create {
		return fmt.Sprintf("The server create outcome is uncertain. Nothing was retried. To deliberately replay the same input, reuse idempotency key %s with input digest sha256:%s; inspect server reads first.", request.idempotencyKey, hex.EncodeToString(digest[:]))
	}
	if request.delete {
		return fmt.Sprintf("The server delete outcome is uncertain. Nothing was retried. Inspect server get %s and its operations; do not blindly reuse ETag %s. Cleanup or remote revocation may remain incomplete.", request.serverID, request.etag)
	}
	return fmt.Sprintf("The server update outcome is uncertain. Nothing was retried. Inspect server get %s; do not blindly reuse ETag %s. Input digest sha256:%s.", request.serverID, request.etag, hex.EncodeToString(digest[:]))
}

func generateIdempotencyKey() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func readServerMutationInput(command *cobra.Command, options *onlineOptions, create bool) ([]byte, map[string]bool, error) {
	allowed := []string{"display_name", "enabled", "transport"}
	if create {
		allowed = append([]string{"namespace"}, allowed...)
	}
	body, err := readOnlineJSONInput(command, options, allowed)
	if err != nil {
		return nil, nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return nil, nil, invalidServerMutationInput(contract.ServerConfigurationFieldConfiguration, contract.ServerConfigurationRuleInvalid)
	}
	if len(object) == 0 && !create {
		return nil, nil, invalidServerMutationInput(contract.ServerConfigurationFieldConfiguration, contract.ServerConfigurationRuleInvalid)
	}
	members := make(map[string]bool, len(object))
	for key := range object {
		members[key] = true
	}
	if create {
		for _, required := range []struct {
			name  string
			field contract.ServerConfigurationField
		}{
			{name: "namespace", field: contract.ServerConfigurationFieldNamespace},
			{name: "display_name", field: contract.ServerConfigurationFieldDisplayName},
			{name: "enabled", field: contract.ServerConfigurationFieldEnabled},
			{name: "transport", field: contract.ServerConfigurationFieldTransport},
		} {
			if !members[required.name] {
				return nil, nil, invalidServerMutationInput(required.field, contract.ServerConfigurationRuleRequired)
			}
		}
		if len(object) != 4 {
			return nil, nil, invalidServerMutationInput(contract.ServerConfigurationFieldConfiguration, contract.ServerConfigurationRuleInvalid)
		}
	}
	if value, ok := object["namespace"]; ok && !jsonString(value) {
		return nil, nil, invalidServerMutationInput(contract.ServerConfigurationFieldNamespace, contract.ServerConfigurationRuleInvalid)
	}
	if value, ok := object["display_name"]; ok && !jsonString(value) {
		return nil, nil, invalidServerMutationInput(contract.ServerConfigurationFieldDisplayName, contract.ServerConfigurationRuleInvalid)
	}
	if value, ok := object["enabled"]; ok && !jsonBoolean(value) {
		return nil, nil, invalidServerMutationInput(contract.ServerConfigurationFieldEnabled, contract.ServerConfigurationRuleInvalid)
	}
	if value, ok := object["transport"]; ok {
		if err := validateServerTransportInput(value); err != nil {
			return nil, nil, err
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var canonical any
	if decoder.Decode(&canonical) != nil {
		return nil, nil, invalidServerMutationInput(contract.ServerConfigurationFieldConfiguration, contract.ServerConfigurationRuleInvalid)
	}
	canonicalBody, err := json.Marshal(canonical)
	if err != nil {
		return nil, nil, invalidServerMutationInput(contract.ServerConfigurationFieldConfiguration, contract.ServerConfigurationRuleInvalid)
	}
	return canonicalBody, members, nil
}

func validateServerTransportInput(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTransport, contract.ServerConfigurationRuleInvalid)
	}
	kindRaw, exists := object["kind"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTransportKind, contract.ServerConfigurationRuleRequired)
	}
	if !jsonString(kindRaw) {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTransportKind, contract.ServerConfigurationRuleInvalid)
	}
	var kind string
	_ = json.Unmarshal(kindRaw, &kind)
	if kind == "stdio" {
		return validateStdioTransportInput(object)
	}
	if kind != "streamable_http" {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTransportKind, contract.ServerConfigurationRuleInvalid)
	}
	return validateHTTPTransportInput(object)
}

func validateStdioTransportInput(object map[string]json.RawMessage) error {
	fields := []struct {
		name  string
		field contract.ServerConfigurationField
		valid func(json.RawMessage) bool
	}{
		{name: "executable", field: contract.ServerConfigurationFieldExecutable, valid: jsonString},
		{name: "arguments", field: contract.ServerConfigurationFieldArguments, valid: jsonStringArray},
		{name: "working_directory", field: contract.ServerConfigurationFieldWorkingDirectory, valid: jsonString},
		{name: "environment", field: contract.ServerConfigurationFieldEnvironment, valid: jsonStringMap},
		{name: "secret_environment", field: contract.ServerConfigurationFieldSecretEnvironment, valid: jsonStringMap},
	}
	for _, field := range fields {
		value, exists := object[field.name]
		if !exists {
			return invalidServerMutationInput(field.field, contract.ServerConfigurationRuleRequired)
		}
		if !field.valid(value) {
			return invalidServerMutationInput(field.field, contract.ServerConfigurationRuleInvalid)
		}
	}
	if !exactMembers(object, "kind", "executable", "arguments", "working_directory", "environment", "secret_environment") {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTransport, contract.ServerConfigurationRuleInvalid)
	}
	return nil
}

func validateHTTPTransportInput(object map[string]json.RawMessage) error {
	for _, field := range []struct {
		name  string
		field contract.ServerConfigurationField
	}{
		{name: "url", field: contract.ServerConfigurationFieldURL},
		{name: "protocol_mode", field: contract.ServerConfigurationFieldProtocolMode},
	} {
		value, exists := object[field.name]
		if !exists {
			return invalidServerMutationInput(field.field, contract.ServerConfigurationRuleRequired)
		}
		if !jsonString(value) {
			return invalidServerMutationInput(field.field, contract.ServerConfigurationRuleInvalid)
		}
	}
	authentication, exists := object["authentication"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldAuthentication, contract.ServerConfigurationRuleRequired)
	}
	if !exactMembers(object, "kind", "url", "protocol_mode", "authentication") {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTransport, contract.ServerConfigurationRuleInvalid)
	}
	return validateServerAuthenticationInput(authentication)
}

func validateServerAuthenticationInput(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return invalidServerMutationInput(contract.ServerConfigurationFieldAuthentication, contract.ServerConfigurationRuleInvalid)
	}
	modeRaw, exists := object["mode"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldAuthenticationMode, contract.ServerConfigurationRuleRequired)
	}
	if !jsonString(modeRaw) {
		return invalidServerMutationInput(contract.ServerConfigurationFieldAuthenticationMode, contract.ServerConfigurationRuleInvalid)
	}
	var mode string
	_ = json.Unmarshal(modeRaw, &mode)
	if mode == "none" || mode == "bearer" {
		if !exactMembers(object, "mode") {
			return invalidServerMutationInput(contract.ServerConfigurationFieldAuthentication, contract.ServerConfigurationRuleInvalid)
		}
		return nil
	}
	if mode != "oauth" {
		return invalidServerMutationInput(contract.ServerConfigurationFieldAuthenticationMode, contract.ServerConfigurationRuleInvalid)
	}
	registration, exists := object["registration"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldRegistration, contract.ServerConfigurationRuleRequired)
	}
	trustedOrigins, exists := object["trusted_origins"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTrustedOrigins, contract.ServerConfigurationRuleRequired)
	}
	if !jsonStringArray(trustedOrigins) {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTrustedOrigins, contract.ServerConfigurationRuleInvalid)
	}
	offlineAccess, exists := object["request_offline_access"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldRequestOfflineAccess, contract.ServerConfigurationRuleRequired)
	}
	if !jsonBoolean(offlineAccess) {
		return invalidServerMutationInput(contract.ServerConfigurationFieldRequestOfflineAccess, contract.ServerConfigurationRuleInvalid)
	}
	if !exactMembers(object, "mode", "registration", "trusted_origins", "request_offline_access") {
		return invalidServerMutationInput(contract.ServerConfigurationFieldAuthentication, contract.ServerConfigurationRuleInvalid)
	}
	return validateServerRegistrationInput(registration)
}

func validateServerRegistrationInput(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return invalidServerMutationInput(contract.ServerConfigurationFieldRegistration, contract.ServerConfigurationRuleInvalid)
	}
	modeRaw, exists := object["mode"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldRegistrationMode, contract.ServerConfigurationRuleRequired)
	}
	if !jsonString(modeRaw) {
		return invalidServerMutationInput(contract.ServerConfigurationFieldRegistrationMode, contract.ServerConfigurationRuleInvalid)
	}
	var mode string
	_ = json.Unmarshal(modeRaw, &mode)
	issuer, exists := object["issuer"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldIssuer, contract.ServerConfigurationRuleRequired)
	}
	if !jsonNullableString(issuer) {
		return invalidServerMutationInput(contract.ServerConfigurationFieldIssuer, contract.ServerConfigurationRuleInvalid)
	}
	if mode == "dynamic" {
		if !exactMembers(object, "mode", "issuer") {
			return invalidServerMutationInput(contract.ServerConfigurationFieldRegistration, contract.ServerConfigurationRuleInvalid)
		}
		return nil
	}
	if mode != "static" {
		return invalidServerMutationInput(contract.ServerConfigurationFieldRegistrationMode, contract.ServerConfigurationRuleInvalid)
	}
	clientID, exists := object["client_id"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldClientID, contract.ServerConfigurationRuleRequired)
	}
	if !jsonString(clientID) {
		return invalidServerMutationInput(contract.ServerConfigurationFieldClientID, contract.ServerConfigurationRuleInvalid)
	}
	authMethod, exists := object["token_endpoint_auth_method"]
	if !exists {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTokenEndpointAuthMethod, contract.ServerConfigurationRuleRequired)
	}
	if !jsonString(authMethod) {
		return invalidServerMutationInput(contract.ServerConfigurationFieldTokenEndpointAuthMethod, contract.ServerConfigurationRuleInvalid)
	}
	if !exactMembers(object, "mode", "issuer", "client_id", "token_endpoint_auth_method") {
		return invalidServerMutationInput(contract.ServerConfigurationFieldRegistration, contract.ServerConfigurationRuleInvalid)
	}
	return nil
}

func exactMembers(object map[string]json.RawMessage, names ...string) bool {
	if len(object) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	return true
}

func jsonString(raw json.RawMessage) bool {
	var value string
	return len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &value) == nil
}
func jsonBoolean(raw json.RawMessage) bool {
	var value bool
	return len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &value) == nil
}
func jsonNullableString(raw json.RawMessage) bool { return string(raw) == "null" || jsonString(raw) }
func jsonStringArray(raw json.RawMessage) bool {
	var value []string
	return len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &value) == nil
}
func jsonStringMap(raw json.RawMessage) bool {
	var value map[string]string
	return len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &value) == nil
}
