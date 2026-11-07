package main

import (
	"net/http"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

//nolint:gosec // Canonical resource kind names, not credential values.
var resourceConfigNamespaces = map[string]string{
	"agent": "agent", "mcp server": "mcp_server", "mcp grant": "mcp_grant",
	"http credential": "http_credential", "http grant": "http_grant",
	"git credential": "git_credential", "git repository": "git_repository", "git grant": "git_grant",
}

func createKeyMembers(spec onlineCommandSpec, members []string) []string {
	if resourceConfigKind(spec) != "" && spec.Path[len(spec.Path)-1] == "create" && resourceConfigKind(spec) != "mcp_server" {
		return append(append([]string(nil), members...), "key")
	}
	return members
}

func resourceConfigKind(spec onlineCommandSpec) string {
	if len(spec.Path) < 2 {
		return ""
	}
	return resourceConfigNamespaces[strings.Join(spec.Path[:len(spec.Path)-1], " ")]
}
func resourceConfigAction(spec onlineCommandSpec) string {
	if resourceConfigKind(spec) == "" {
		return ""
	}
	action := spec.Path[len(spec.Path)-1]
	if action == "import" || action == "export" {
		return action
	}
	return ""
}
func resourceConfigSpecs() []onlineCommandSpec {
	var specs []onlineCommandSpec
	// Stable ordering keeps command manifests and help deterministic.
	for _, namespace := range []string{"agent", "mcp server", "mcp grant", "http credential", "http grant", "git credential", "git repository", "git grant"} {
		for _, action := range []string{"import", "export"} {
			path := append(strings.Fields(namespace), action)
			spec := onlineCommandSpec{Path: path, Use: "export KEY", ManifestUse: namespace + " export KEY", Short: "Export a resource configuration"}
			if action == "import" {
				spec.Use = "import"
				spec.ManifestUse = namespace + " import --file PATH [--dry-run | --confirm]"
				spec.Short = "Create a resource from configuration"
				spec.Flags = []string{"file", "dry-run", "confirm"}
				spec.RequiredFlags = []string{"file"}
			}
			specs = append(specs, spec)
		}
	}
	return specs
}

func prepareResourceConfigInput(command *cobra.Command, spec onlineCommandSpec, options *onlineOptions) (onlineIntent, *controlclient.OnlineError) {
	if options.dryRun && options.confirm {
		return onlineIntent{}, controlclient.NewInputError("Choose --dry-run or --confirm, not both.")
	}
	if !options.dryRun && !options.confirm {
		return onlineIntent{}, controlclient.NewInputError("Creation requires --confirm; use --dry-run for read-only validation. Nothing was submitted.")
	}
	if options.file == "-" && options.bearerStdin {
		return onlineIntent{}, controlclient.NewInputError("Standard input cannot provide both configuration and the administrator bearer.")
	}
	body, err := controlclient.ReadConfigInput(controlclient.InputOptions{Path: options.file, Stdin: command.InOrStdin(), AllowedMembers: []string{"format", "version", "kind", "key", "reserved", "spec"}})
	if err != nil {
		return onlineIntent{}, controlclient.NewInputError("Supply exactly one matching version-2 definition via --file PATH or --file -. Use top-level import for bundles or multiple resources.", err)
	}
	var definition contract.ConfigDefinition
	if controlclient.DecodeConfigResponse(body, &definition) != nil || definition.Kind != resourceConfigKind(spec) || !validResourceConfigDefinition(definition) {
		return onlineIntent{}, controlclient.NewInputError("Supply exactly one matching version-2 definition. Use top-level import for bundles or multiple resources; dependencies must already exist.")
	}
	return onlineIntent{body: body, file: true}, nil
}
func validResourceConfigDefinition(d contract.ConfigDefinition) bool {
	if !contract.ConfigResourceKind(d.Kind) || contract.ValidateConfigDefinitionEnvelope(d) != nil {
		return false
	}
	if d.Reserved {
		return true
	}
	spec := contract.ConfigSpecDestination(d.Kind)
	if spec == nil || controlclient.DecodeConfigResponse(d.Spec, spec) != nil {
		return false
	}
	server, ok := spec.(*contract.ConfigServerSpec)
	return !ok || server.Deleted || validateServerTransportInput(server.Transport) == nil
}

func runResourceConfig(command *cobra.Command, spec onlineCommandSpec, options *onlineOptions, args []string) error {
	kind := resourceConfigKind(spec)
	if resourceConfigAction(spec) == "export" {
		return runConfigExportAt(command, options, contract.ConfigResourcePath+kind+"/"+args[0], kind, args[0])
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value, JSONBody: true})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	path, status := contract.ConfigImportPath, http.StatusCreated
	if options.dryRun {
		path, status = contract.ConfigPreviewPath, http.StatusOK
	}
	uncertain := func() error {
		return writeOnlineFailure(command, options.output, &controlclient.OnlineError{Code: "client_outcome_uncertain", Title: "The configuration creation outcome is uncertain. Nothing was replayed. Inspect resources by typed key and server status before any new action.", Exit: 8, Uncertain: true})
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodPost, Path: path, Header: header, Body: options.intent.body})
	if err != nil {
		failure := controlclient.ClassifyClientError(err)
		if options.dryRun {
			failure = classifyReadFailure(err)
		} else if failure.Uncertain {
			return uncertain()
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	if response.StatusCode != status {
		failure := evaluateOnlineResponse(response, options.adminBearer.path)
		if !options.dryRun && (failure == nil || failure.Code == "storage_unavailable" || failure.Code == "client_response_invalid") {
			return uncertain()
		}
		if failure == nil {
			failure = controlclient.ClassifyClientError(controlclient.ErrResponseInvalid)
		}
		return writeOnlineFailure(command, options.output, failure)
	}
	var input contract.ConfigDefinition
	_ = controlclient.DecodeConfigResponse(options.intent.body, &input)
	table := controlclient.Table{}
	valid := response.Header.Get("Content-Type") == contract.MediaTypeJSON
	if options.dryRun {
		var preview contract.ConfigPreview
		valid = valid && controlclient.DecodeResponse(response.Body, &preview) == nil && preview.Advisory && len(preview.Creations) == 1
		if valid {
			creation := preview.Creations[0]
			valid = creation.Kind == kind && creation.Key == input.Key && creation.Reserved == input.Reserved && creation.Dependencies != nil && creation.Requirements != nil && validConfigPreviewCreation(creation)
			dependencies := make([]string, 0, len(creation.Dependencies))
			for _, ref := range creation.Dependencies {
				dependencies = append(dependencies, ref.Kind+"/"+ref.Key)
			}
			table = controlclient.Table{Headers: []string{"INTENT", "KIND", "KEY", "DEPENDENCIES", "REQUIREMENTS", "LIMITATION"}, Rows: [][]string{{"Create (advisory)", kind, input.Key, strings.Join(dependencies, ", "), strings.Join(creation.Requirements, ", "), "No mutation; credentials and activation unverified"}}}
		}
	} else {
		var result contract.ConfigImportResult
		valid = valid && controlclient.DecodeResponse(response.Body, &result) == nil && result.Configuration == "committed" && len(result.Creations) == 1 && result.Activation == resourceConfigActivation(input)
		if valid {
			creation := result.Creations[0]
			valid = creation.Kind == kind && creation.Key == input.Key && creation.Reserved == input.Reserved && (creation.Reserved && creation.ID == "" || !creation.Reserved && gatewayIDPattern.MatchString(creation.ID))
			table = controlclient.Table{Headers: []string{"CONFIGURATION", "KIND", "KEY", "ID", "ACTIVATION", "NEXT"}, Rows: [][]string{{result.Configuration, kind, input.Key, creation.ID, result.Activation, "Credentials, client trust and upstream readiness unverified; inspect resource status"}}}
		}
	}
	if !valid {
		if !options.dryRun {
			return uncertain()
		}
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(controlclient.ErrResponseInvalid))
	}
	if err := controlclient.WriteSuccess(command.OutOrStdout(), controlclient.OutputMode(options.output), response.Body, table); err != nil {
		if !options.dryRun {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("Configuration committed; output delivery failed. Inspect resources by typed key; do not replay.", err))
		}
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The preview output could not be written.", err))
	}
	return nil
}
func resourceConfigActivation(d contract.ConfigDefinition) string {
	if d.Kind == "mcp_server" && !d.Reserved {
		var server contract.ConfigServerSpec
		if controlclient.DecodeConfigResponse(d.Spec, &server) == nil && server.Enabled {
			return "pending"
		}
	}
	return "not_required"
}

func validConfigPreviewCreation(c contract.ConfigCreation) bool {
	if len(c.Dependencies) > 2 || len(c.Requirements) > 4 {
		return false
	}
	for _, ref := range c.Dependencies {
		if ref.Kind == "gateway_mcp" {
			if ref.Key != "" {
				return false
			}
			continue
		}
		if !contract.ConfigResourceKind(ref.Kind) || !contract.ValidResourceKey(ref.Key) {
			return false
		}
	}
	for _, requirement := range c.Requirements {
		switch requirement {
		case "issue_agent_credential", "supply_credential_value", "supply_static_credentials", "supply_oauth_client_secret", "authorize_oauth_explicitly", "activate_after_commit", "initialize_untouched_routing_seed":
		default:
			return false
		}
	}
	return true
}
