package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var gitRepositoryETagPattern = regexp.MustCompile(`^"git-repository-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)
var gitCredentialETagPattern = regexp.MustCompile(`^"git-credential-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)
var gitGrantETagPattern = regexp.MustCompile(`^"git-grant-([0-7][0-9A-HJKMNP-TV-Z]{25})-([1-9][0-9]*)"$`)
var gitProfileETagPattern = regexp.MustCompile(`^"git-profile-(routing)-([1-9][0-9]*)"$`)

func gitOnlineSpecs() []onlineCommandSpec {
	var specs []onlineCommandSpec
	for _, group := range []string{"repository", "grant", "credential"} {
		actions := []string{"list", "get", "create", "update", "delete"}
		if group == "credential" {
			actions = append(actions, "rotate")
		}
		for _, action := range actions {
			use, manifest := action, "git "+group+" "+action
			flags := []string{}
			var required []string
			switch action {
			case "list":
				flags = []string{"limit", "cursor"}
			case "get":
				use += " ID"
				manifest += " ID"
			case "create":
				manifest += " --file PATH"
				flags = []string{"file", "yes"}
				required = []string{"file"}
			case "update", "rotate":
				use += " ID"
				manifest += " ID --file PATH [--etag ETAG]"
				flags = []string{"file", "etag", "yes"}
				required = []string{"file"}
			case "delete":
				use += " ID"
				manifest += " ID [--etag ETAG]"
				flags = []string{"etag", "yes"}
			}
			verb := strings.ToUpper(action[:1]) + action[1:]
			description := verb + " a Git " + group
			if action == "list" {
				description = "List Git " + group + "s"
				if group == "repository" {
					description = "List Git repositories"
				}
			}
			specs = append(specs, onlineCommandSpec{Path: []string{"git", group, action}, Use: use, ManifestUse: manifest, Short: description, Flags: flags, RequiredFlags: required})
		}
	}
	return append(specs,
		onlineCommandSpec{Path: []string{"git", "routing-profile", "get"}, Use: "get", ManifestUse: "git routing-profile get", Short: "Get Git routing intent"},
		onlineCommandSpec{Path: []string{"git", "routing-profile", "update"}, Use: "update", ManifestUse: "git routing-profile update --file PATH [--etag ETAG]", Short: "Update Git routing intent", Flags: []string{"file", "etag", "yes"}, RequiredFlags: []string{"file"}},
	)
}

func gitWireETag(group, id, revision string) string {
	return `"git-` + group + `-` + id + `-` + revision + `"`
}
func validGitTimes(created, updated string) bool {
	c, ok := httpResponseTime(created)
	u, valid := httpResponseTime(updated)
	return ok && valid && !u.Before(c)
}
func validGitLocatorResponse(raw string) bool {
	if len(raw) == 0 || len(raw) > contract.GitLocatorBytes || strings.ContainsAny(raw, "%?#\\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Path == "" || u.Path == "/" || strings.HasSuffix(u.Path, "/") {
		return false
	}
	host, port := u.Hostname(), u.Port()
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != port || net.JoinHostPort(host, port) != u.Host || strings.HasPrefix(host, "*.") || !validHTTPResponseHost(host) {
		return false
	}
	for _, segment := range strings.Split(u.Path[1:], "/") {
		if segment == "" || segment == "." || segment == ".." || strings.Trim(segment, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~") != "" {
			return false
		}
	}
	return u.String() == raw
}
func validGitRepository(g contract.GitRepository) bool {
	if !contract.ValidAuditID(g.ID) || !validCanonicalRevision(g.Revision) || g.Revision == "0" || !validCanonicalRevision(g.AliasRevision) || g.AliasRevision == "0" || !validGrantDescription(g.Name) || !validGitLocatorResponse(g.URL) || g.Aliases == nil || len(g.Aliases) > contract.GitAliases || g.CredentialID != nil && !contract.ValidAuditID(*g.CredentialID) || !validGitTimes(g.CreatedAt, g.UpdatedAt) {
		return false
	}
	base := strings.TrimSuffix(g.URL, ".git")
	for i, alias := range g.Aliases {
		if alias == g.URL || alias != base && alias != base+".git" || i > 0 && g.Aliases[i-1] >= alias {
			return false
		}
	}
	return true
}
func validGitGrant(g contract.GitGrant) bool {
	if !contract.ValidAuditID(g.ID) || !contract.ValidAuditID(g.PrincipalID) || !contract.ValidAuditID(g.RepositoryID) || !validCanonicalRevision(g.Revision) || g.Revision == "0" || g.Description != nil && !validGrantDescription(*g.Description) || !validGitTimes(g.CreatedAt, g.UpdatedAt) || g.State != contract.GrantActive && g.State != contract.GrantExpired || g.Policy.Version != 1 || g.Policy.Refs == nil || len(g.Policy.Refs) > contract.GitRules || len(g.Policy.Refs) > 0 && !g.Policy.Read {
		return false
	}
	if g.ExpiresAt != nil {
		expiry, ok := httpResponseTime(*g.ExpiresAt)
		created, _ := httpResponseTime(g.CreatedAt)
		if !ok || !expiry.After(created) {
			return false
		}
	} else if g.State == contract.GrantExpired {
		return false
	}
	for i, rule := range g.Policy.Refs {
		value := rule.Ref.Value
		switch rule.Ref.Kind {
		case "exact":
		case "prefix":
			if !strings.HasSuffix(value, "/") || len(value) > contract.GitRefBytes {
				return false
			}
			value += "x"
		default:
			return false
		}
		if !validGitRefResponse(value) || len(rule.Actions) == 0 || len(rule.Actions) > 3 || i > 0 && g.Policy.Refs[i-1].Ref.Kind+":"+g.Policy.Refs[i-1].Ref.Value >= rule.Ref.Kind+":"+rule.Ref.Value {
			return false
		}
		for j, action := range rule.Actions {
			if action != "create" && action != "delete" && action != "update" || j > 0 && rule.Actions[j-1] >= action {
				return false
			}
		}
	}
	return true
}

// Validate the closed wire representation without consulting policy authority.
func validGitRefResponse(value string) bool {
	if len(value) > contract.GitRefBytes || !strings.HasPrefix(value, "refs/") || strings.Count(value, "/") < 2 || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e || strings.ContainsRune("~^:?*[\\", rune(b)) {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return false
		}
	}
	return true
}

func validGitProfile(g contract.GitRoutingProfile) bool {
	if !validCanonicalRevision(g.Revision) || g.Revision == "0" || g.Active || g.Origins == nil || len(g.Origins) > contract.GitRepositories {
		return false
	}
	for i, origin := range g.Origins {
		if !validGitLocatorResponse(origin+"/repository") || i > 0 && g.Origins[i-1] >= origin {
			return false
		}
	}
	return true
}
func gitRepositoryTable(body []byte) (controlclient.Table, error) {
	var g contract.GitRepository
	if controlclient.DecodeExactResponse(body, &g) != nil || !validGitRepository(g) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	return controlclient.Table{Headers: []string{"NAME", "ID", "URL", "REVISION"}, Rows: [][]string{{g.Name, g.ID, g.URL, g.Revision}}}, nil
}
func gitGrantTable(body []byte) (controlclient.Table, error) {
	var g contract.GitGrant
	if controlclient.DecodeExactResponse(body, &g) != nil || !validGitGrant(g) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	policy, _ := json.Marshal(g.Policy)
	return controlclient.Table{Headers: []string{"ID", "AGENT", "REPOSITORY", "POLICY"}, Rows: [][]string{{g.ID, g.PrincipalID, g.RepositoryID, string(policy)}}}, nil
}
func validGitCredential(c contract.GitCredential) bool {
	if !contract.ValidAuditID(c.ID) || !validCanonicalRevision(c.Revision) || c.Revision == "0" || !validGrantDescription(c.Name) || !validGitLocatorResponse(c.Origin+"/repository") || !contract.ValidHTTPCredentialRecipe(c.Recipe) || c.Recipe.Header != http.CanonicalHeaderKey(c.Recipe.Header) || c.References == nil || len(c.References) > contract.GitRepositoryIdentities || !validGitTimes(c.CreatedAt, c.UpdatedAt) {
		return false
	}
	for i, ref := range c.References {
		if !contract.ValidAuditID(ref.ID) || i > 0 && c.References[i-1].ID >= ref.ID {
			return false
		}
	}
	return true
}
func gitCredentialTable(body []byte) (controlclient.Table, error) {
	var c contract.GitCredential
	if controlclient.DecodeExactResponse(body, &c) != nil || !validGitCredential(c) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	return controlclient.Table{Headers: []string{"NAME", "ID", "ORIGIN", "AVAILABLE", "REFERENCES"}, Rows: [][]string{{c.Name, c.ID, c.Origin, strconv.FormatBool(c.Available), strconv.Itoa(len(c.References))}}}, nil
}
func gitProfileTable(body []byte) (controlclient.Table, error) {
	var g contract.GitRoutingProfile
	if controlclient.DecodeExactResponse(body, &g) != nil || !validGitProfile(g) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	return controlclient.Table{Headers: []string{"ORIGINS", "REVISION", "ACTIVE"}, Rows: [][]string{{strings.Join(g.Origins, ", "), g.Revision, "false"}}}, nil
}
func gitListTable[T any](body []byte, maximum int, table func([]byte) (controlclient.Table, error)) (controlclient.Table, error) {
	var page contract.QueryCollection[T]
	if controlclient.DecodeExactResponse(body, &page) != nil || page.Items == nil || len(page.Items) > 100 || page.TotalCount < 0 || page.TotalCount > maximum || page.Offset < 0 || page.Offset > page.TotalCount-len(page.Items) || (page.NextCursor != nil) != (page.Offset+len(page.Items) < page.TotalCount) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	result := controlclient.Table{}
	for _, g := range page.Items {
		raw, _ := json.Marshal(g)
		row, err := table(raw)
		if err != nil {
			return result, err
		}
		result.Headers = row.Headers
		result.Rows = append(result.Rows, row.Rows...)
	}
	return withNextCursor(result, page.NextCursor), nil
}

func runGitPolicy(cmd *cobra.Command, options *onlineOptions, args []string, group, action string) error {
	kind, path, table := onlineItemGitRepository, "/api/v2/git/repositories", gitRepositoryTable
	if group == "grant" {
		kind, path, table = onlineItemGitGrant, "/api/v2/git/grants", gitGrantTable
	}
	if group == "credential" {
		kind, path, table = onlineItemGitCredential, "/api/v2/git/credentials", gitCredentialTable
	}
	profile := group == "routing-profile"
	if profile {
		path = "/api/v2/git/routing-profile"
		table = gitProfileTable
	}
	if action == "list" {
		listPath, err := controlclient.BuildListPath(path, controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
		if err != nil {
			return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
		}
		if group == "repository" {
			return runOnlineRead(cmd, options, listPath, func(b []byte) (controlclient.Table, error) {
				return gitListTable[contract.GitRepository](b, contract.GitRepositories, table)
			})
		}
		if group == "credential" {
			return runOnlineRead(cmd, options, listPath, func(b []byte) (controlclient.Table, error) {
				return gitListTable[contract.GitCredential](b, contract.GitCredentials, table)
			})
		}
		return runOnlineRead(cmd, options, listPath, func(b []byte) (controlclient.Table, error) {
			return gitListTable[contract.GitGrant](b, contract.GitGrants, table)
		})
	}
	id := ""
	if !profile && action != "create" {
		id = args[0]
		path += "/" + id
	}
	if action == "get" {
		if profile {
			return runOnlineRead(cmd, options, path, table)
		}
		return runOnlineItemRead(cmd, options, kind, id, table)
	}
	var body []byte
	var err error
	if action != "delete" {
		fields := []string{"name", "url", "aliases", "credential_id"}
		if group == "grant" {
			fields = []string{"principal_id", "repository_id", "description", "policy", "expires_at"}
		}
		if profile {
			fields = []string{"origins"}
		}
		if group == "credential" {
			fields = []string{"name", "origin", "recipe"}
			if action == "create" {
				fields = append(fields, "secret")
			}
			if action == "rotate" {
				fields = []string{"secret"}
			}
		}
		body, err = readOnlineJSONInput(cmd, options, fields)
		if err != nil {
			return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
		}
	} else if group == "credential" {
		body = []byte(`{}`)
	}
	defer clear(body)
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: "Change Git configuration or authority? Production Git enforcement remains inactive. Never replay an uncertain mutation."}); err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	etag := ""
	if id != "" {
		var failure *controlclient.OnlineError
		etag, failure = resolveMutationETag(cmd, options, kind, id)
		if failure != nil {
			return writeOnlineFailure(cmd, options.output, failure)
		}
	}
	if profile {
		etag = options.etag
		if etag == "" {
			headers, _ := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value})
			response, e := client.Do(cmd.Context(), controlclient.Request{Method: http.MethodGet, Path: path, Header: headers})
			if e != nil {
				return writeOnlineFailure(cmd, options.output, controlclient.ClassifyRequestError(e, controlclient.RequestPhasePreflight))
			}
			if failure := evaluateOnlineResponse(response, options.adminBearer.path); failure != nil {
				return writeOnlineFailure(cmd, options.output, failure)
			}
			var current contract.GitRoutingProfile
			if response.StatusCode != http.StatusOK || controlclient.DecodeExactResponse(response.Body, &current) != nil || !validGitProfile(current) || response.Header.Get("ETag") != gitWireETag("profile", "routing", current.Revision) {
				return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(controlclient.ErrResponseInvalid))
			}
			etag = response.Header.Get("ETag")
		}
	}
	method, status := http.MethodPost, http.StatusCreated
	if action == "update" {
		method, status = http.MethodPatch, http.StatusOK
	}
	if action == "delete" {
		method, status = http.MethodDelete, http.StatusNoContent
	}
	if action == "rotate" {
		path += "/rotate"
		method, status = http.MethodPost, http.StatusOK
	}
	headers, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: body != nil, ETag: etag})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(cmd.Context(), controlclient.Request{Method: method, Path: path, Header: headers, Body: body})
	if err != nil {
		return writeOnlineFailure(cmd, options.output, controlclient.ClassifyRequestError(err, controlclient.RequestPhaseMutation))
	}
	uncertain := func() error {
		return writeOnlineFailure(cmd, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: "The Git mutation result is uncertain. Inspect current state; do not replay.", Exit: 8, Uncertain: true})
	}
	if response.StatusCode != status {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid" {
			return uncertain()
		}
		return writeOnlineFailure(cmd, options.output, failure)
	}
	mode, _ := controlclient.ParseOutputMode(options.output)
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
	if profile {
		var g contract.GitRoutingProfile
		if controlclient.DecodeExactResponse(response.Body, &g) != nil || response.Header.Get("ETag") != gitWireETag("profile", "routing", g.Revision) {
			return uncertain()
		}
	} else {
		if id == "" {
			var identity struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(response.Body, &identity) != nil {
				return uncertain()
			}
			id = identity.ID
		}
		if !validateOnlineItem(kind, id, response.Header.Get("ETag"), response.Body) {
			return uncertain()
		}
	}
	return controlclient.WriteSuccess(cmd.OutOrStdout(), mode, response.Body, rendered)
}
