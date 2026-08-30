package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/spf13/cobra"
)

const maxRotationRecoveryCommandBytes = 220

var adminFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

type adminRotationSuccess struct {
	Result        string                   `json:"result"`
	OldCredential contract.AdminCredential `json:"old_credential"`
	NewCredential contract.AdminCredential `json:"new_credential"`
}

func runAdminCredentialRotate(command *cobra.Command, options *onlineOptions, args []string) error {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The old administrator credential ID is invalid."))
	}
	oldID := args[0]
	if rendered, err := renderRotationRecoveryCommand("01ARZ3NDEKTSV4RRFFQ69G5FAV", options.secretOutput); err != nil || len(rendered) > maxRotationRecoveryCommandBytes {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The replacement bearer path is too long to render safe recovery guidance."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	oldCredential, failure := loadRotationCredential(command, client, options.adminBearer.value, options.adminBearer.path, oldID)
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	if oldCredential.Status != contract.CredentialActive {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The old administrator credential is not active."))
	}
	authority, authorityETag, failure := loadRotationAuthority(command, client, options.adminBearer.value, options.adminBearer.path)
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	if authorityETag != contract.AdminAuthorityETag(authority.Revision) {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The administrator authority response is invalid."))
	}
	sink, err := controlclient.PrepareSensitiveSink(controlclient.SinkOptions{Path: options.secretOutput})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	defer func() { _ = sink.Cleanup() }()
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Create and durably verify a replacement administrator credential before conditionally revoking the named old credential?"}); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}

	created, createETag, failure := submitRotationCreate(command, client, options, sink, oldID, authorityETag)
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	reopenedBearer, err := sink.PublishAdminRotation(created.Bearer, created.AdminCredential)
	if err != nil {
		title := rotationRecoveryTitle("The replacement credential "+created.ID+" was created, but its bearer could not be durably published and verified. The old credential was not revoked.", created.ID, options, command)
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "admin_rotation_publish_failed", Title: title, Exit: 2})
	}
	verifiedReplacement, failure := loadRotationCredential(command, client, reopenedBearer, options.secretOutput, created.ID)
	if failure != nil || !reflect.DeepEqual(verifiedReplacement, created.AdminCredential) || verifiedReplacement.Status != contract.CredentialActive || !verifiedReplacement.NonExpiring || verifiedReplacement.ExpiresAt != nil {
		exit, status := rotationVerificationClass(failure)
		title := rotationRecoveryTitle("The replacement credential "+created.ID+" was published, but replacement authentication or metadata verification failed. The old credential was not revoked.", created.ID, options, command)
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Status: status, Code: "admin_rotation_replacement_verification_failed", Title: title, Exit: exit})
	}

	completion, completionETag, response, requestErr := submitRotationCompletion(command, client, reopenedBearer, oldID, created.ID, createETag)
	if requestErr != nil {
		failure = controlclient.ClassifyClientError(requestErr)
		if failure.Code == "client_outcome_uncertain" {
			return recoverUncertainRotation(command, client, options, reopenedBearer, oldID, verifiedReplacement)
		}
		return writeOnlineFailure(command, options.output, rotationIncompleteFailure(failure, created.ID, options, command))
	}
	if responseFailure := evaluateOnlineResponse(response, options.secretOutput); responseFailure != nil {
		if responseFailure.Code == "storage_unavailable" || responseFailure.Code == "client_response_invalid" {
			return recoverUncertainRotation(command, client, options, reopenedBearer, oldID, verifiedReplacement)
		}
		return writeOnlineFailure(command, options.output, rotationIncompleteFailure(responseFailure, created.ID, options, command))
	}
	if response.StatusCode != http.StatusOK || !validRotationCompletion(completion, oldID, verifiedReplacement) || completionETag != contract.AdminAuthorityETag(completion.OldCredential.Revision) {
		return recoverUncertainRotation(command, client, options, reopenedBearer, oldID, verifiedReplacement)
	}

	finalOld, failure := loadRotationCredential(command, client, reopenedBearer, options.secretOutput, oldID)
	if failure != nil || !reflect.DeepEqual(finalOld, completion.OldCredential) {
		return writeRotationFinalVerificationFailure(command, options, created.ID, failure)
	}
	finalNew, failure := loadRotationCredential(command, client, reopenedBearer, options.secretOutput, created.ID)
	if failure != nil || !reflect.DeepEqual(finalNew, completion.NewCredential) {
		return writeRotationFinalVerificationFailure(command, options, created.ID, failure)
	}
	finalAuthority, finalETag, failure := loadRotationAuthority(command, client, reopenedBearer, options.secretOutput)
	if failure != nil || finalETag != completionETag || finalAuthority.Revision != finalOld.Revision {
		return writeRotationFinalVerificationFailure(command, options, created.ID, failure)
	}
	return writeAdminRotationSuccess(command, options, finalOld, finalNew)
}

func submitRotationCreate(command *cobra.Command, client *controlclient.Client, options *onlineOptions, sink *controlclient.PreparedSink, oldID, authorityETag string) (contract.CreatedAdminCredential, string, *controlclient.OnlineError) {
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, ETag: authorityETag, JSONBody: true})
	if err != nil {
		return contract.CreatedAdminCredential{}, "", controlclient.ClassifyClientError(err)
	}
	sink.MarkSubmitted()
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/admin-credentials", Header: header, Body: []byte(`{"expires_at":null}`)})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if failure.Code == "client_outcome_uncertain" {
			return contract.CreatedAdminCredential{}, "", adminRotationCreateUncertain()
		}
		return contract.CreatedAdminCredential{}, "", failure
	}
	if failure := evaluateOnlineResponse(response, options.adminBearer.path); failure != nil {
		if failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			return contract.CreatedAdminCredential{}, "", adminRotationCreateUncertain()
		}
		return contract.CreatedAdminCredential{}, "", failure
	}
	var created contract.CreatedAdminCredential
	if response.StatusCode != http.StatusCreated || controlclient.DecodeResponse(response.Body, &created) != nil || !validRotationReplacement(created, oldID) {
		return contract.CreatedAdminCredential{}, "", adminRotationCreateUncertain()
	}
	etag := response.Header.Get("ETag")
	if etag != contract.AdminAuthorityETag(created.Revision) {
		return contract.CreatedAdminCredential{}, "", adminRotationCreateUncertain()
	}
	return created, etag, nil
}

func submitRotationCompletion(command *cobra.Command, client *controlclient.Client, bearer, oldID, replacementID, authorityETag string) (contract.AdminCredentialRotationResult, string, controlclient.Response, error) {
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: bearer, ETag: authorityETag, JSONBody: true})
	if err != nil {
		return contract.AdminCredentialRotationResult{}, "", controlclient.Response{}, err
	}
	body, _ := json.Marshal(contract.AdminCredentialRotationCompletion{ReplacementID: replacementID})
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: "/api/v1/admin-credentials/" + oldID + "/rotation-completion", Header: header, Body: body})
	if err != nil {
		return contract.AdminCredentialRotationResult{}, "", controlclient.Response{}, err
	}
	var completion contract.AdminCredentialRotationResult
	if response.StatusCode == http.StatusOK {
		_ = controlclient.DecodeResponse(response.Body, &completion)
	}
	return completion, response.Header.Get("ETag"), response, nil
}

func loadRotationCredential(command *cobra.Command, client *controlclient.Client, bearer, bearerPath, id string) (contract.AdminCredential, *controlclient.OnlineError) {
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: bearer})
	if err != nil {
		return contract.AdminCredential{}, controlclient.ClassifyClientError(err)
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodGet, Path: "/api/v1/admin-credentials/" + id, Header: header})
	if err != nil {
		return contract.AdminCredential{}, classifyReadFailure(err)
	}
	if failure := evaluateOnlineResponse(response, bearerPath); failure != nil {
		return contract.AdminCredential{}, failure
	}
	var credential contract.AdminCredential
	if response.StatusCode != http.StatusOK || controlclient.DecodeResponse(response.Body, &credential) != nil || credential.ID != id || !validRotationCredential(credential) {
		return contract.AdminCredential{}, &controlclient.OnlineError{Code: "client_response_invalid", Title: "The Gateway response is invalid.", Exit: 10}
	}
	return credential, nil
}

func loadRotationAuthority(command *cobra.Command, client *controlclient.Client, bearer, bearerPath string) (contract.AdminAuthority, string, *controlclient.OnlineError) {
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: bearer})
	if err != nil {
		return contract.AdminAuthority{}, "", controlclient.ClassifyClientError(err)
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodGet, Path: "/api/v1/admin-authority", Header: header})
	if err != nil {
		return contract.AdminAuthority{}, "", classifyReadFailure(err)
	}
	if failure := evaluateOnlineResponse(response, bearerPath); failure != nil {
		return contract.AdminAuthority{}, "", failure
	}
	var authority contract.AdminAuthority
	if response.StatusCode != http.StatusOK || controlclient.DecodeResponse(response.Body, &authority) != nil || !validCanonicalRevision(authority.Revision) {
		return contract.AdminAuthority{}, "", &controlclient.OnlineError{Code: "client_response_invalid", Title: "The Gateway response is invalid.", Exit: 10}
	}
	etag := response.Header.Get("ETag")
	if etag != contract.AdminAuthorityETag(authority.Revision) {
		return contract.AdminAuthority{}, "", &controlclient.OnlineError{Code: "client_response_invalid", Title: "The Gateway response is invalid.", Exit: 10}
	}
	return authority, etag, nil
}

func recoverUncertainRotation(command *cobra.Command, client *controlclient.Client, options *onlineOptions, replacementBearer, oldID string, verifiedReplacement contract.AdminCredential) error {
	old, failure := loadRotationCredential(command, client, replacementBearer, options.secretOutput, oldID)
	if failure == nil && old.Status == contract.CredentialRevoked {
		return writeAdminRotationSuccess(command, options, old, verifiedReplacement)
	}
	title := rotationRecoveryTitle("The old credential revoke outcome is uncertain. Nothing was replayed. One safe metadata read did not prove a completed revoke.", verifiedReplacement.ID, options, command)
	return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "admin_rotation_revoke_uncertain", Title: title, Exit: 8, Uncertain: true})
}

func writeRotationFinalVerificationFailure(command *cobra.Command, options *onlineOptions, replacementID string, cause *controlclient.OnlineError) error {
	exit, status := rotationVerificationClass(cause)
	title := rotationRecoveryTitle("Rotation completion returned, but final credential or authority verification failed. Nothing was replayed.", replacementID, options, command)
	return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Status: status, Code: "admin_rotation_final_verification_failed", Title: title, Exit: exit})
}

func rotationVerificationClass(failure *controlclient.OnlineError) (int, *int) {
	if failure == nil {
		return 10, nil
	}
	return failure.Exit, failure.Status
}

func rotationIncompleteFailure(failure *controlclient.OnlineError, replacementID string, options *onlineOptions, command *cobra.Command) *controlclient.OnlineError {
	if failure == nil {
		failure = &controlclient.OnlineError{Code: "client_response_invalid", Exit: 10}
	}
	copy := *failure
	copy.Title = rotationRecoveryTitle("The replacement credential "+replacementID+" remains active and the old credential was not confirmed revoked. Nothing was replayed.", replacementID, options, command)
	return &copy
}

func adminRotationCreateUncertain() *controlclient.OnlineError {
	return &controlclient.OnlineError{Code: "admin_rotation_create_uncertain", Title: "The replacement credential create outcome is uncertain. Nothing was replayed. The old credential remains selected; inspect administrator credential metadata, but metadata cannot recover the bearer or prove publication.", Exit: 8, Uncertain: true}
}

func validRotationReplacement(created contract.CreatedAdminCredential, oldID string) bool {
	return created.ID != oldID && created.Status == contract.CredentialActive && created.NonExpiring && created.ExpiresAt == nil && adminBearerPattern.MatchString(created.Bearer) && validRotationCredential(created.AdminCredential)
}

func validRotationCompletion(result contract.AdminCredentialRotationResult, oldID string, replacement contract.AdminCredential) bool {
	return result.OldCredential.ID == oldID && result.OldCredential.Status == contract.CredentialRevoked && result.NewCredential.ID == replacement.ID && result.NewCredential.Status == contract.CredentialActive && result.NewCredential.NonExpiring && result.NewCredential.ExpiresAt == nil && reflect.DeepEqual(result.NewCredential, replacement) && validRotationCredential(result.OldCredential)
}

func validRotationCredential(credential contract.AdminCredential) bool {
	return validAdminCredential(credential) && adminFingerprintPattern.MatchString(credential.Fingerprint) && credential.CreatedAt != "" && (credential.NonExpiring == (credential.ExpiresAt == nil))
}

func renderRotationRecoveryCommand(replacementID, path string) (string, error) {
	return renderBearerCommand("mcp-gateway admin credential get "+replacementID, path)
}

func rotationRecoveryTitle(prefix, replacementID string, options *onlineOptions, command *cobra.Command) string {
	rendered, _ := renderRotationRecoveryCommand(replacementID, options.secretOutput)
	title := prefix + " Continue with " + rendered + "."
	if rotationUsesDefaultBearer(command, options) {
		title += " The default bearer file still names the old credential."
	}
	return title
}

func rotationUsesDefaultBearer(command *cobra.Command, options *onlineOptions) bool {
	dataDir, err := command.Root().PersistentFlags().GetString("data-dir")
	if err != nil {
		return false
	}
	layout, err := gatewaypaths.Resolve(dataDir)
	return err == nil && options.adminBearer.path == layout.AdminBearer
}

func writeAdminRotationSuccess(command *cobra.Command, options *onlineOptions, oldCredential, newCredential contract.AdminCredential) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The output mode is invalid."))
	}
	if mode == controlclient.OutputJSON {
		body, err := json.Marshal(adminRotationSuccess{Result: "rotated", OldCredential: oldCredential, NewCredential: newCredential})
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command output could not be encoded."))
		}
		return controlclient.WriteSuccess(command.OutOrStdout(), mode, body, controlclient.Table{})
	}
	recovery, _ := renderRotationRecoveryCommand(newCredential.ID, options.secretOutput)
	note := "Use the new owner-only bearer file for later online commands."
	if rotationUsesDefaultBearer(command, options) {
		note += " The default bearer file still names the old credential."
	}
	return controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, controlclient.Table{
		Headers: []string{"RESULT", "OLD_ID", "OLD_STATUS", "NEW_ID", "NEW_STATUS", "NEXT_COMMAND", "NOTE"},
		Rows:    [][]string{{"rotated", oldCredential.ID, string(oldCredential.Status), newCredential.ID, string(newCredential.Status), recovery, note}},
	})
}
