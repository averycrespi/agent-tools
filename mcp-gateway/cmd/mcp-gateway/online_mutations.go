package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var serverETagPattern = regexp.MustCompile(`^"server-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)

type serverMutationWire struct {
	Server    serverWire                `json:"server"`
	Operation *contract.ServerOperation `json:"operation"`
}

func runServerCreate(command *cobra.Command, options *onlineOptions) error {
	body, _, err := readServerMutationInput(command, options, true)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("Server creation requires exactly namespace, display_name, enabled, and a closed transport object."))
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
	matches := serverETagPattern.FindStringSubmatch(options.etag)
	if len(matches) != 3 || matches[1] != args[0] {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ETag is invalid or belongs to another server."))
	}
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{
		Yes:         options.yes,
		Consequence: "Permanently delete this server identity, withdraw its routes, invalidate local authority, and schedule cleanup? Remote revocation is best effort and cannot be guaranteed.",
	}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	return runServerMutation(command, options, serverMutationRequest{
		method: http.MethodDelete, path: "/api/v1/servers/" + args[0], body: []byte("{}"), etag: options.etag,
		successStatuses: map[int]struct{}{http.StatusOK: {}, http.StatusAccepted: {}}, serverID: args[0], delete: true,
	})
}

func runServerUpdate(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ID is invalid."))
	}
	matches := serverETagPattern.FindStringSubmatch(options.etag)
	if len(matches) != 3 || matches[1] != args[0] {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ETag is invalid or belongs to another server."))
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
	return runServerMutation(command, options, serverMutationRequest{
		method: http.MethodPatch, path: "/api/v1/servers/" + args[0], body: body, etag: options.etag,
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
	body, err := controlclient.ReadJSONInput(controlclient.InputOptions{Path: options.file, Stdin: command.InOrStdin(), AllowedMembers: allowed})
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
	if create && (!members["namespace"] || !members["display_name"] || !members["enabled"] || !members["transport"] || len(object) != 4) {
		return nil, nil, controlclient.ErrInvalidInput
	}
	if value, ok := object["namespace"]; ok && !jsonString(value) {
		return nil, nil, controlclient.ErrInvalidInput
	}
	if value, ok := object["display_name"]; ok && !jsonString(value) {
		return nil, nil, controlclient.ErrInvalidInput
	}
	if value, ok := object["enabled"]; ok && !jsonBoolean(value) {
		return nil, nil, controlclient.ErrInvalidInput
	}
	if value, ok := object["transport"]; ok && !validTransport(value) {
		return nil, nil, controlclient.ErrInvalidInput
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var canonical any
	if decoder.Decode(&canonical) != nil {
		return nil, nil, controlclient.ErrInvalidInput
	}
	canonicalBody, err := json.Marshal(canonical)
	if err != nil {
		return nil, nil, controlclient.ErrInvalidInput
	}
	return canonicalBody, members, nil
}

func validTransport(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || !jsonString(object["kind"]) {
		return false
	}
	var kind string
	_ = json.Unmarshal(object["kind"], &kind)
	if kind == "stdio" {
		return exactMembers(object, "kind", "executable", "arguments", "working_directory", "environment", "secret_environment") && jsonString(object["executable"]) && jsonStringArray(object["arguments"]) && jsonString(object["working_directory"]) && jsonStringMap(object["environment"]) && jsonStringMap(object["secret_environment"])
	}
	if kind != "streamable_http" || !exactMembers(object, "kind", "url", "protocol_mode", "authentication") || !jsonString(object["url"]) || !jsonString(object["protocol_mode"]) {
		return false
	}
	return validAuthentication(object["authentication"])
}

func validAuthentication(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || !jsonString(object["mode"]) {
		return false
	}
	var mode string
	_ = json.Unmarshal(object["mode"], &mode)
	if mode == "none" || mode == "bearer" {
		return exactMembers(object, "mode")
	}
	if mode != "oauth" || !exactMembers(object, "mode", "registration", "trusted_origins", "request_offline_access") || !jsonStringArray(object["trusted_origins"]) || !jsonBoolean(object["request_offline_access"]) {
		return false
	}
	var registration map[string]json.RawMessage
	if json.Unmarshal(object["registration"], &registration) != nil || !jsonString(registration["mode"]) {
		return false
	}
	var registrationMode string
	_ = json.Unmarshal(registration["mode"], &registrationMode)
	if registrationMode == "dynamic" {
		return exactMembers(registration, "mode", "issuer") && jsonNullableString(registration["issuer"])
	}
	return registrationMode == "static" && exactMembers(registration, "mode", "issuer", "client_id", "token_endpoint_auth_method") && jsonNullableString(registration["issuer"]) && jsonString(registration["client_id"]) && jsonString(registration["token_endpoint_auth_method"])
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
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil
}
func jsonBoolean(raw json.RawMessage) bool {
	var value bool
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil
}
func jsonNullableString(raw json.RawMessage) bool { return string(raw) == "null" || jsonString(raw) }
func jsonStringArray(raw json.RawMessage) bool {
	var value []string
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil
}
func jsonStringMap(raw json.RawMessage) bool {
	var value map[string]string
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil
}
