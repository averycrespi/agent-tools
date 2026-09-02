package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func runBackupCreate(command *cobra.Command, options *onlineOptions) error {
	key := options.idempotencyKey
	var err error
	if key == "" {
		key, err = generateIdempotencyKey()
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("An idempotency key could not be generated."))
		}
	}
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, IdempotencyKey: key})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/backups", Header: header, Body: []byte("{}")})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = backupCreateUncertainTitle(key)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: backupCreateUncertainTitle(key), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || len(response.Body) == 0 {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: backupCreateUncertainTitle(key), Exit: 8, Uncertain: true})
	}
	var backup contract.Backup
	if controlclient.DecodeResponse(response.Body, &backup) != nil || !validBackup(backup) {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: backupCreateUncertainTitle(key), Exit: 8, Uncertain: true})
	}
	if mode == controlclient.OutputJSON {
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, backupTable([]contract.Backup{backup}))
}

func runBackupDelete(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The backup ID is invalid."))
	}
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Permanently delete this verified backup artifact? Restore from it will no longer be possible."}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true})
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodDelete, Path: "/api/v1/backups/" + args[0], Header: header, Body: []byte("{}")})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			failure.Title = backupDeleteUncertainTitle(args[0])
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			failure = &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: backupDeleteUncertainTitle(args[0]), Exit: 8, Uncertain: true}
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if options.output == string(controlclient.OutputJSON) {
		return controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputJSON, []byte("{}"), controlclient.Table{})
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputTable, nil, controlclient.Table{Headers: []string{"ID", "RESULT"}, Rows: [][]string{{args[0], "deleted"}}})
}

func backupCreateUncertainTitle(key string) string {
	digest := sha256.Sum256([]byte("{}"))
	return "The backup create outcome is uncertain. Nothing was replayed. Inspect backup list before deliberately replaying POST /api/v1/backups with idempotency key " + key + " and input digest sha256:" + hex.EncodeToString(digest[:]) + "."
}

func backupDeleteUncertainTitle(id string) string {
	return "The backup delete outcome is uncertain. Nothing was replayed. Inspect backup get " + id + " before deciding whether to make a new confirmed attempt."
}

func validBackup(backup contract.Backup) bool {
	return gatewayIDPattern.MatchString(backup.ID) && gatewayIDPattern.MatchString(backup.InstallationID) && validCanonicalRevision(backup.SourceRevision) && backup.SchemaVersion != "" && backup.SizeBytes >= 0 && len(backup.SHA256) == 64
}

func backupListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.Backup]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return withNextCursor(backupTable(page.Items), page.NextCursor), nil
}

func backupItemTable(body []byte) (controlclient.Table, error) {
	var backup contract.Backup
	if err := controlclient.DecodeResponse(body, &backup); err != nil {
		return controlclient.Table{}, err
	}
	return backupTable([]contract.Backup{backup}), nil
}

func backupTable(backups []contract.Backup) controlclient.Table {
	rows := make([][]string, 0, len(backups))
	for _, backup := range backups {
		rows = append(rows, []string{backup.ID, backup.CreatedAt, backup.InstallationID, backup.SchemaVersion, backup.SourceRevision, strconv.FormatInt(backup.SizeBytes, 10), backup.SHA256})
	}
	return controlclient.Table{Headers: []string{"ID", "CREATED", "INSTALLATION", "SCHEMA", "SOURCE_REVISION", "BYTES", "SHA256"}, Rows: rows}
}
