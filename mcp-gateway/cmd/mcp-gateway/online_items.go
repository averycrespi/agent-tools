package main

import (
	"net/http"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

type onlineItemKind uint8

const (
	onlineItemServer onlineItemKind = iota
	onlineItemPrincipal
	onlineItemGrantRequest
)

type validatedOnlineItem struct {
	Body []byte
	ETag string
}

func resolveMutationETag(command *cobra.Command, options *onlineOptions, kind onlineItemKind, id string) (string, *controlclient.OnlineError) {
	if options == nil {
		return "", controlclient.NewInputError("The mutation precondition is invalid.")
	}
	if options.etag != "" {
		if !validItemETag(kind, id, options.etag) {
			return "", controlclient.NewInputError("The ETag is invalid or belongs to another resource.")
		}
		return options.etag, nil
	}
	item, failure := loadValidatedItem(command, options, kind, id, controlclient.RequestPhasePreflight)
	if failure != nil {
		return "", failure
	}
	return item.ETag, nil
}

func validItemETag(kind onlineItemKind, id, etag string) bool {
	var parts []string
	switch kind {
	case onlineItemServer:
		parts = serverETagPattern.FindStringSubmatch(etag)
	case onlineItemPrincipal:
		parts = principalETagPattern.FindStringSubmatch(etag)
	case onlineItemGrantRequest:
		parts = grantRequestETagPattern.FindStringSubmatch(etag)
	default:
		return false
	}
	return len(parts) == 3 && parts[1] == id
}

func loadValidatedItem(command *cobra.Command, options *onlineOptions, kind onlineItemKind, id string, phase controlclient.RequestPhase) (validatedOnlineItem, *controlclient.OnlineError) {
	path, ok := onlineItemPath(kind, id)
	if command == nil || options == nil || !ok || !gatewayIDPattern.MatchString(id) {
		return validatedOnlineItem{}, controlclient.NewInputError("The item selection is invalid.")
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return validatedOnlineItem{}, controlclient.ClassifyRequestError(err, phase)
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value})
	if err != nil {
		return validatedOnlineItem{}, controlclient.ClassifyClientError(err)
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodGet, Path: path, Header: header})
	if err != nil {
		return validatedOnlineItem{}, controlclient.ClassifyRequestError(err, phase)
	}
	if failure := evaluateOnlineResponse(response, options.adminBearer.path); failure != nil {
		return validatedOnlineItem{}, failure
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != controlclient.MediaTypeJSON || !validateOnlineItem(kind, id, response.Header.Get("ETag"), response.Body) {
		return validatedOnlineItem{}, controlclient.ClassifyRequestError(controlclient.ErrResponseInvalid, phase)
	}
	return validatedOnlineItem{Body: append([]byte(nil), response.Body...), ETag: response.Header.Get("ETag")}, nil
}

func onlineItemPath(kind onlineItemKind, id string) (string, bool) {
	switch kind {
	case onlineItemServer:
		return "/api/v1/servers/" + id, true
	case onlineItemPrincipal:
		return "/api/v1/principals/" + id, true
	case onlineItemGrantRequest:
		return "/api/v1/grant-requests/" + id, true
	default:
		return "", false
	}
}

func validateOnlineItem(kind onlineItemKind, id, etag string, body []byte) bool {
	switch kind {
	case onlineItemServer:
		var server serverWire
		return controlclient.DecodeResponse(body, &server) == nil && server.ID == id && validCanonicalRevision(server.DesiredRevision) && contract.MatchesServerETag(etag, server.ID, server.DesiredRevision)
	case onlineItemPrincipal:
		var principal contract.Principal
		return controlclient.DecodeResponse(body, &principal) == nil && principal.ID == id && validPrincipal(principal) && contract.MatchesPrincipalETag(etag, principal.ID, principal.Revision)
	case onlineItemGrantRequest:
		var request contract.GrantRequest
		return controlclient.DecodeResponse(body, &request) == nil && request.ID == id && validGrantRequest(request) && contract.MatchesGrantRequestETag(etag, request.ID, request.Revision)
	default:
		return false
	}
}

func runOnlineItemRead(command *cobra.Command, options *onlineOptions, kind onlineItemKind, id string, table func([]byte) (controlclient.Table, error)) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputHuman), controlclient.NewInputError("The output mode is invalid."))
	}
	item, failure := loadValidatedItem(command, options, kind, id, controlclient.RequestPhaseRead)
	if failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	if mode == controlclient.OutputJSON {
		if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, item.Body, controlclient.Table{}); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return nil
	}
	projection, err := table(item.Body)
	if err != nil || len(projection.Rows) != 1 {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(controlclient.ErrResponseInvalid))
	}
	projection.Headers = append(projection.Headers, "ETAG")
	projection.Rows[0] = append(projection.Rows[0], item.ETag)
	if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, projection); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command output could not be written."))
	}
	return nil
}
