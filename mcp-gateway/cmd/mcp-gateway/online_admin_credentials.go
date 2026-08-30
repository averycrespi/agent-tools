package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var adminBearerPattern = regexp.MustCompile(`^mgw_admin_[A-Za-z0-9_-]{43}$`)

func runAdminCredentialCreate(command *cobra.Command, options *onlineOptions) error {
	body, err := readAdminCredentialCreateInput(command, options)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	sink, err := controlclient.PrepareSensitiveSink(controlclient.SinkOptions{Path: options.secretOutput})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	defer func() { _ = sink.Cleanup() }()
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	sink.MarkSubmitted()
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/admin-credentials", Header: header, Body: body})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = adminCredentialCreateUncertainTitle()
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if failure := evaluateOnlineResponse(response, options.adminBearer.path); failure != nil {
		if failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: adminCredentialCreateUncertainTitle(), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusCreated {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: adminCredentialCreateUncertainTitle(), Exit: 8, Uncertain: true})
	}
	var created contract.CreatedAdminCredential
	if controlclient.DecodeResponse(response.Body, &created) != nil || !validAdminCredential(created.AdminCredential) || !adminBearerPattern.MatchString(created.Bearer) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: adminCredentialCreateUncertainTitle(), Exit: 8, Uncertain: true})
	}
	if err := sink.Publish(created.Bearer); err != nil {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_secret_sink_unavailable", Title: "The admin credential was created, but its one-time bearer could not be published. The bearer cannot be recovered; inspect credential metadata before deciding whether to revoke it.", Exit: 2})
	}
	return writeAdminCredentialSuccess(command, options, created.AdminCredential)
}

func readAdminCredentialCreateInput(command *cobra.Command, options *onlineOptions) ([]byte, error) {
	body, err := readOnlineJSONInput(command, options, []string{"expires_at"})
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || len(object) != 1 || object["expires_at"] == nil {
		return nil, controlclient.ErrInvalidInput
	}
	if string(object["expires_at"]) == "null" {
		return []byte(`{"expires_at":null}`), nil
	}
	var expiresAt string
	if json.Unmarshal(object["expires_at"], &expiresAt) != nil {
		return nil, controlclient.ErrInvalidInput
	}
	expiry, err := time.Parse(time.RFC3339, expiresAt)
	now := time.Now()
	if err != nil || expiry.Before(now.Add(5*time.Minute)) || expiry.After(now.Add(365*24*time.Hour)) {
		return nil, controlclient.ErrInvalidInput
	}
	return json.Marshal(struct {
		ExpiresAt string `json:"expires_at"`
	}{ExpiresAt: expiresAt})
}

func runAdminCredentialRevoke(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The admin credential ID is invalid."))
	}
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Revoke this admin credential and close every browser session derived from it? The final active non-expiring credential cannot be revoked."}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true})
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodDelete, Path: "/api/v1/admin-credentials/" + args[0], Header: header, Body: []byte("{}")})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = adminCredentialRevokeUncertainTitle(args[0])
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: adminCredentialRevokeUncertainTitle(args[0]), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if options.output == string(controlclient.OutputJSON) {
		return controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputJSON, []byte("{}"), controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputTable, nil, controlclient.Table{Headers: []string{"ID", "RESULT"}, Rows: [][]string{{args[0], "revoked"}}})
}

func writeAdminCredentialSuccess(command *cobra.Command, options *onlineOptions, credential contract.AdminCredential) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	if mode == controlclient.OutputJSON {
		body, err := json.Marshal(credential)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command output could not be encoded."))
		}
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, body, controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, adminCredentialTable([]contract.AdminCredential{credential}))
}

func adminCredentialCreateUncertainTitle() string {
	return "The admin credential create outcome is uncertain. Nothing was replayed. Inspect admin credential list and get; metadata cannot recover the bearer or prove it was published."
}

func adminCredentialRevokeUncertainTitle(id string) string {
	return "The admin credential revoke outcome is uncertain. Nothing was replayed. Inspect admin credential get " + id + "; only current metadata can guide an explicit next action."
}

func validAdminCredential(credential contract.AdminCredential) bool {
	if !gatewayIDPattern.MatchString(credential.ID) || credential.Fingerprint == "" || !validCanonicalRevision(credential.Revision) {
		return false
	}
	switch credential.Status {
	case contract.CredentialActive, contract.CredentialRevoked, contract.CredentialExpired:
		return true
	default:
		return false
	}
}

func adminCredentialListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.AdminCredential]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return adminCredentialTable(page.Items), nil
}

func adminCredentialItemTable(body []byte) (controlclient.Table, error) {
	var credential contract.AdminCredential
	if err := controlclient.DecodeResponse(body, &credential); err != nil {
		return controlclient.Table{}, err
	}
	return adminCredentialTable([]contract.AdminCredential{credential}), nil
}

func adminCredentialTable(credentials []contract.AdminCredential) controlclient.Table {
	rows := make([][]string, 0, len(credentials))
	for _, credential := range credentials {
		rows = append(rows, []string{credential.ID, credential.Fingerprint, string(credential.Status), credential.Revision, credential.CreatedAt, pointerText(credential.ExpiresAt)})
	}
	return controlclient.Table{Headers: []string{"ID", "FINGERPRINT", "STATUS", "REVISION", "CREATED", "EXPIRES"}, Rows: rows}
}
