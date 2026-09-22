package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var httpGrantETagPattern = regexp.MustCompile(`^"http-grant-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)
var httpDefaultETagPattern = regexp.MustCompile(`^"http-default-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)

func validHTTPDefault(d contract.PrincipalHTTPDefault) bool {
	return contract.ValidAuditID(d.PrincipalID) && validCanonicalRevision(d.Revision) && d.Revision != "0" && (d.Default == contract.HTTPDefaultAllow || d.Default == contract.HTTPDefaultBlock)
}
func httpGrantTable(body []byte) (controlclient.Table, error) {
	var g contract.HTTPGrant
	if controlclient.DecodeExactResponse(body, &g) != nil || !validHTTPGrant(g) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	description := ""
	if g.Description != nil {
		description = *g.Description
	}
	return controlclient.Table{Headers: []string{"ID", "DESCRIPTION", "PRINCIPAL", "STATE", "POLICY"}, Rows: [][]string{{g.ID, description, g.PrincipalID, string(g.State), string(g.Policy)}}}, nil
}
func httpDefaultTable(body []byte) (controlclient.Table, error) {
	var d contract.PrincipalHTTPDefault
	if controlclient.DecodeExactResponse(body, &d) != nil || !validHTTPDefault(d) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	return controlclient.Table{Headers: []string{"PRINCIPAL", "HTTP DEFAULT"}, Rows: [][]string{{d.PrincipalID, string(d.Default)}}}, nil
}
func httpGrantListTable(body []byte) (controlclient.Table, error) {
	var page contract.QueryCollection[contract.HTTPGrantTableItem]
	if controlclient.DecodeExactResponse(body, &page) != nil || page.Items == nil || len(page.Items) > 100 || page.TotalCount > contract.HTTPPolicyGrants || page.TotalCount < 0 || page.Offset < 0 || page.Offset > page.TotalCount-len(page.Items) || (page.NextCursor != nil) != (page.Offset+len(page.Items) < page.TotalCount) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	if page.NextCursor != nil && (*page.NextCursor == "" || len(*page.NextCursor) > 512 || containsControl(*page.NextCursor)) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	table := controlclient.Table{Headers: []string{"ID", "PRINCIPAL", "STATE", "POLICY"}}
	seen := map[string]bool{}
	for _, item := range page.Items {
		if !validHTTPGrant(item.Grant) || seen[item.Grant.ID] || len(item.PrincipalDisplayName) == 0 || len(item.PrincipalDisplayName) > 256 || !utf8.ValidString(item.PrincipalDisplayName) || containsControl(item.PrincipalDisplayName) {
			return table, controlclient.ErrResponseInvalid
		}
		seen[item.Grant.ID] = true
		table.Rows = append(table.Rows, []string{item.Grant.ID, item.PrincipalDisplayName, string(item.Grant.State), string(item.Grant.Policy)})
	}
	return withNextCursor(table, page.NextCursor), nil
}
func httpPreviewTable(body []byte) (controlclient.Table, error) {
	var p contract.HTTPAccessPreview
	if controlclient.DecodeExactResponse(body, &p) != nil || !validHTTPPreview(p) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	evidence := []string{"policy revision " + strconv.FormatUint(p.Decision.PolicyRevision, 10), "default revision " + strconv.FormatUint(p.Decision.DefaultRevision, 10)}
	for _, item := range []struct {
		name string
		ref  *contract.HTTPRevisionRef
	}{{"principal", &p.Decision.Principal}, {"grant", p.Decision.Grant}, {"private grant", p.Decision.PrivateGrant}, {"credential", p.Decision.Credential}, {"credential grant", p.Decision.CredentialGrant}, {"conflict credential", p.Decision.ConflictCredential}, {"conflict grant", p.Decision.ConflictGrant}} {
		if item.ref != nil {
			evidence = append(evidence, item.name+" "+item.ref.ID+"@"+strconv.FormatUint(item.ref.Revision, 10))
		}
	}
	return controlclient.Table{Headers: []string{"POLICY ELIGIBLE", "TRANSPORT", "REASON", "HTTP DEFAULT", "EVIDENCE", "LIMITATION"}, Rows: [][]string{{strconv.FormatBool(p.Decision.Allowed), string(p.Decision.Transport), string(p.Decision.Reason), string(p.Default), strings.Join(evidence, "; "), "Policy only: network, TLS and material unverified; no admission authority"}}}, nil
}

func runHTTPPolicy(cmd *cobra.Command, options *onlineOptions, args []string, group, action string) error {
	kind := onlineItemHTTPGrant
	path := "/api/v2/http/grants"
	table := httpGrantTable
	if group == "default" {
		kind = onlineItemHTTPDefault
		path = "/api/v2/http/defaults"
		table = httpDefaultTable
	}
	preview := group == "test-access"
	if preview {
		path = "/api/v2/http/access-preview"
		table = httpPreviewTable
	}
	if action == "list" {
		p, err := controlclient.BuildListPath(path, controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
		if err != nil {
			return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(cmd, options, p, httpGrantListTable)
	}
	id := ""
	if action != "create" && !preview {
		if len(args) != 1 || !contract.ValidAuditID(args[0]) {
			return writeOnlineFailure(cmd, options.output, controlclient.NewInputError("A valid HTTP resource ID is required."))
		}
		id = args[0]
		path += "/" + id
	}
	if action == "get" {
		return runOnlineItemRead(cmd, options, kind, id, table)
	}
	var body []byte
	if action != "delete" {
		fields := []string{"principal_id", "description", "policy", "expires_at"}
		if group == "default" {
			fields = []string{"default"}
		}
		if preview {
			fields = []string{"principal_id", "url", "method", "connect"}
		}
		var err error
		body, err = readOnlineJSONInput(cmd, options, fields)
		if err != nil {
			return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
		}
	}
	defer clear(body)
	if !preview {
		if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Change HTTP access policy? Tunnel allows bypass request policy and credential injection. Never replay an uncertain mutation."}); err != nil {
			return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
		}
	}
	etag := ""
	if id != "" {
		var failure *controlclient.OnlineError
		etag, failure = resolveMutationETag(cmd, options, kind, id)
		if failure != nil {
			return writeOnlineFailure(cmd, options.output, failure)
		}
	}
	method, status := http.MethodPost, http.StatusOK
	if action == "create" {
		status = http.StatusCreated
	}
	if action == "update" {
		method = http.MethodPatch
	}
	if action == "delete" {
		method = http.MethodDelete
		status = http.StatusNoContent
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	headers, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: body != nil, ETag: etag})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	phase := controlclient.RequestPhaseMutation
	if preview {
		phase = controlclient.RequestPhaseRead
	}
	response, err := client.Do(cmd.Context(), controlclient.Request{Method: method, Path: path, Header: headers, Body: body})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyRequestError(err, phase))
	}
	uncertain := func() error {
		if preview {
			return writeOnlineFailure(cmd, options.output, controlclient.ClassifyRequestError(controlclient.ErrResponseInvalid, phase))
		}
		return writeOnlineFailure(cmd, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: "The HTTP policy result is uncertain. Inspect current state; do not replay.", Exit: 8, Uncertain: true})
	}
	if response.StatusCode != status {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || !preview && (failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid") {
			return uncertain()
		}
		return writeOnlineFailure(cmd, options.output, failure)
	}
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	if action == "delete" {
		if len(response.Body) != 0 {
			return uncertain()
		}
		return controlclient.WriteSuccess(cmd.OutOrStdout(), mode, []byte(`{"deleted":true}`), controlclient.Table{Headers: []string{"RESULT"}, Rows: [][]string{{"Deleted"}}})
	}
	rendered, err := table(response.Body)
	if err != nil || response.Header.Get("Content-Type") != contract.MediaTypeJSON {
		return uncertain()
	}
	if preview {
		var input struct {
			PrincipalID string          `json:"principal_id"`
			Connect     json.RawMessage `json:"connect"`
		}
		var result contract.HTTPAccessPreview
		if json.Unmarshal(body, &input) != nil || controlclient.DecodeExactResponse(response.Body, &result) != nil || input.PrincipalID != result.Decision.Principal.ID || (len(input.Connect) == 0) != (result.Decision.Transport == contract.HTTPTransportRequest) {
			return uncertain()
		}
	} else {
		if kind == onlineItemHTTPGrant {
			var g contract.HTTPGrant
			var input struct {
				PrincipalID string `json:"principal_id"`
			}
			if controlclient.DecodeExactResponse(response.Body, &g) != nil || json.Unmarshal(body, &input) != nil || input.PrincipalID != g.PrincipalID {
				return uncertain()
			}
			if id == "" {
				id = g.ID
			}
		}
		if !validateOnlineItem(kind, id, response.Header.Get("ETag"), response.Body) {
			return uncertain()
		}
	}
	return controlclient.WriteSuccess(cmd.OutOrStdout(), mode, response.Body, rendered)
}
