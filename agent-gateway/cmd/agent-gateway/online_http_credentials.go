package main

import (
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
)

var httpCredentialETagPattern = regexp.MustCompile(`^"http-credential-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)

func validHTTPCredential(r contract.HTTPCredential) bool {
	if !contract.ValidAuditID(r.ID) || !validCanonicalRevision(r.Revision) || r.Revision == "0" || r.Name == "" || len(r.Name) > contract.HTTPCredentialNameBytes || r.Boundary.Host == "" || r.Boundary.Port == 0 || !contract.ValidHTTPCredentialRecipe(r.Recipe) || r.References == nil || len(r.References) > contract.HTTPPolicyGrants {
		return false
	}
	for _, ref := range r.References {
		if !contract.ValidAuditID(ref.ID) {
			return false
		}
	}
	_, created := time.Parse(time.RFC3339Nano, r.CreatedAt)
	_, updated := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return created == nil && updated == nil
}
func httpCredentialRow(r contract.HTTPCredential) []string {
	return []string{r.Name, r.ID, r.Boundary.Host + ":" + strconv.Itoa(int(r.Boundary.Port)), r.Recipe.Header, r.Recipe.Prefix, strconv.FormatBool(r.Available), strconv.Itoa(len(r.References))}
}
func httpCredentialItemTable(body []byte) (controlclient.Table, error) {
	var resource contract.HTTPCredential
	if controlclient.DecodeResponse(body, &resource) != nil || !validHTTPCredential(resource) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	return controlclient.Table{Headers: []string{"NAME", "ID", "HTTPS BOUNDARY", "HEADER", "PREFIX", "AVAILABLE", "REFERENCES"}, Rows: [][]string{httpCredentialRow(resource)}}, nil
}
func httpCredentialListTable(body []byte) (controlclient.Table, error) {
	var page contract.QueryCollection[contract.HTTPCredential]
	if controlclient.DecodeResponse(body, &page) != nil || page.Items == nil || len(page.Items) > 100 || page.TotalCount < 0 || page.Offset < 0 || page.Offset > page.TotalCount-len(page.Items) || (page.NextCursor != nil) != (page.Offset+len(page.Items) < page.TotalCount) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	table := controlclient.Table{Headers: []string{"NAME", "ID", "HTTPS BOUNDARY", "HEADER", "PREFIX", "AVAILABLE", "REFERENCES"}}
	for _, r := range page.Items {
		if !validHTTPCredential(r) {
			return controlclient.Table{}, controlclient.ErrResponseInvalid
		}
		table.Rows = append(table.Rows, httpCredentialRow(r))
	}
	return table, nil
}

func runHTTPCredential(cmd *cobra.Command, options *onlineOptions, args []string, action string) error {
	path := "/api/v2/http/credentials"
	if action == "list" {
		path, err := controlclient.BuildListPath(path, controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
		if err != nil {
			return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(cmd, options, path, httpCredentialListTable)
	}
	id := ""
	if action != "create" {
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(cmd, options.output, controlclient.NewInputError("The HTTP credential ID is invalid."))
		}
		id = args[0]
		path += "/" + id
	}
	if action == "get" {
		return runOnlineItemRead(cmd, options, onlineItemHTTPCredential, id, httpCredentialItemTable)
	}
	method := http.MethodPost
	body := []byte("{}")
	expected := http.StatusOK
	if action != "delete" {
		members := []string{"name", "boundary", "recipe"}
		switch action {
		case "create":
			members = append(members, "secret")
		case "rotate":
			members = []string{"secret"}
		}
		var err error
		body, err = readOnlineJSONInput(cmd, options, members)
		if err != nil {
			return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
		}
	}
	defer clear(body)
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Change this scoped HTTP credential? Rotation may leave it unavailable on failure; referenced credentials cannot be deleted. Never replay an uncertain result."}); err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	etag := ""
	if action != "create" {
		var failure *controlclient.OnlineError
		etag, failure = resolveMutationETag(cmd, options, onlineItemHTTPCredential, id)
		if failure != nil {
			return writeOnlineFailure(cmd, options.output, failure)
		}
	}
	switch action {
	case "create":
		expected = http.StatusCreated
	case "update":
		method = http.MethodPatch
	case "rotate":
		path += "/rotate"
	case "delete":
		method = http.MethodDelete
		expected = http.StatusNoContent
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true, ETag: etag})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(cmd.Context(), controlclient.Request{Method: method, Path: path, Header: header, Body: body})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	uncertain := func() error {
		return writeOnlineFailure(cmd, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: "The HTTP credential change is uncertain. Inspect current metadata; do not replay the secret submission.", Exit: 8, Uncertain: true})
	}
	if response.StatusCode != expected {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "keyring_unavailable" || failure.Code == "client_response_invalid" {
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
	var resource contract.HTTPCredential
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || controlclient.DecodeResponse(response.Body, &resource) != nil || !validHTTPCredential(resource) || id != "" && resource.ID != id || response.Header.Get("ETag") != contract.HTTPCredentialETag(resource.ID, resource.Revision) {
		return uncertain()
	}
	table, err := httpCredentialItemTable(response.Body)
	if err != nil {
		return uncertain()
	}
	return controlclient.WriteSuccess(cmd.OutOrStdout(), mode, response.Body, table)
}
