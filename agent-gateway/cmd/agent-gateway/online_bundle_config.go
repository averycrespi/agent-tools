package main

import (
	"encoding/json"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func prepareBundleConfigInput(command *cobra.Command, options *onlineOptions) (onlineIntent, *controlclient.OnlineError) {
	body, err := controlclient.ReadConfigInput(controlclient.InputOptions{Path: options.file, Stdin: command.InOrStdin(), AllowedMembers: []string{"format", "version", "resources", "kind", "key", "reserved", "spec"}})
	invalid := func() (onlineIntent, *controlclient.OnlineError) {
		return onlineIntent{}, controlclient.NewInputError("Supply one strict version-2 bundle or individual definition via --file PATH or --file -. Initialize and start the receiving Gateway first; inspect typed keys and references with --dry-run.")
	}
	if err != nil {
		return onlineIntent{}, controlclient.NewInputError("Read one strict version-2 configuration document via --file PATH or --file -.", err)
	}
	defs := configInputDefinitions(body)
	if defs == nil {
		return invalid()
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if !validResourceConfigDefinition(d) {
			return invalid()
		}
		identity := d.Kind + "/" + d.Key
		if seen[identity] {
			return invalid()
		}
		seen[identity] = true
	}
	return onlineIntent{body: body, file: true}, nil
}

// Partial submissions may refer to existing receiving resources. Complete-export
// graph validation would wrongly require every dependency (and routing) here.
func configInputDefinitions(body []byte) []contract.ConfigDefinition {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	if _, bundle := envelope["resources"]; bundle {
		var b contract.ConfigBundle
		if controlclient.DecodeConfigResponse(body, &b) != nil || b.Format != contract.ConfigExportFormat || b.Version != 2 || b.Resources == nil || len(b.Resources) > contract.ConfigV2KeyBindings+1025 {
			return nil
		}
		return b.Resources
	}
	var d contract.ConfigDefinition
	if controlclient.DecodeConfigResponse(body, &d) != nil {
		return nil
	}
	return []contract.ConfigDefinition{d}
}

func configImportTable(response controlclient.Response, inputs []contract.ConfigDefinition, dry bool) (controlclient.Table, bool) {
	table := controlclient.Table{}
	if response.Header.Get("Content-Type") != contract.MediaTypeJSON || inputs == nil {
		return table, false
	}
	expected := map[string]contract.ConfigDefinition{}
	activation := "not_required"
	for _, d := range inputs {
		expected[d.Kind+"/"+d.Key] = d
		if resourceConfigActivation(d) == "pending" {
			activation = "pending"
		}
	}
	seen := map[string]bool{}
	match := func(kind, key string, reserved bool) bool {
		identity := kind + "/" + key
		d, ok := expected[identity]
		if !ok || seen[identity] || d.Reserved != reserved {
			return false
		}
		seen[identity] = true
		return true
	}
	if dry {
		var preview contract.ConfigPreview
		if controlclient.DecodeConfigResponse(response.Body, &preview) != nil || !preview.Advisory || preview.Creations == nil || len(preview.Creations) != len(inputs) {
			return table, false
		}
		table.Headers = []string{"INTENT", "KIND", "KEY", "RESERVED", "DEPENDENCIES", "REQUIREMENTS", "LIMITATION"}
		for _, c := range preview.Creations {
			if !match(c.Kind, c.Key, c.Reserved) || c.Dependencies == nil || c.Requirements == nil || !validConfigPreviewCreation(c) {
				return table, false
			}
			dependencies := make([]string, 0, len(c.Dependencies))
			for _, ref := range c.Dependencies {
				dependencies = append(dependencies, strings.TrimSuffix(ref.Kind+"/"+ref.Key, "/"))
			}
			table.Rows = append(table.Rows, []string{"Create (advisory)", c.Kind, c.Key, configReservedLabel(c.Reserved), strings.Join(dependencies, ", "), strings.Join(c.Requirements, ", "), "No mutation; credentials and activation unverified"})
		}
		if len(inputs) == 0 {
			table.Notes = []string{"Empty advisory preview; no creations or mutation"}
		}
	} else {
		var result contract.ConfigImportResult
		if controlclient.DecodeConfigResponse(response.Body, &result) != nil || result.Configuration != "committed" || result.Activation != activation || result.Creations == nil || len(result.Creations) != len(inputs) {
			return table, false
		}
		table.Headers = []string{"CONFIGURATION", "KIND", "KEY", "RESERVED", "ID", "ACTIVATION", "NEXT"}
		ids := map[string]bool{}
		for _, c := range result.Creations {
			noID := c.Kind == "git_routing"
			if !match(c.Kind, c.Key, c.Reserved) || noID && c.ID != "" || !noID && (!gatewayIDPattern.MatchString(c.ID) || ids[c.ID]) {
				return table, false
			}
			if c.ID != "" {
				ids[c.ID] = true
			}
			table.Rows = append(table.Rows, []string{result.Configuration, c.Kind, c.Key, configReservedLabel(c.Reserved), c.ID, result.Activation, "Credentials, client trust and upstream readiness unverified; inspect resource status"})
		}
		if len(inputs) == 0 {
			table.Notes = []string{"Configuration committed; no creations; activation not_required"}
		}
	}
	return table, true
}
func configReservedLabel(reserved bool) string {
	if reserved {
		return "yes"
	}
	return "no"
}
