package main

import (
	"encoding/json"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

type onlineDirectFlag struct {
	name     string
	values   []string
	required bool
	toggle   bool
}

type onlineIntentSpec struct {
	fileMembers   []string
	direct        []onlineDirectFlag
	conflicts     [][]string
	defaultDirect bool
	buildBody     func(map[string]string, map[string]bool, map[string]bool) ([]byte, error)
}

type onlineIntent struct {
	body    []byte
	file    bool
	strings map[string]string
	bools   map[string]bool
	changed map[string]bool
}

func prepareOnlineIntent(command *cobra.Command, spec onlineCommandSpec, options *onlineOptions, args []string) (onlineIntent, *controlclient.OnlineError) {
	if failure := validateOnlineLocalOptions(command, spec, options, args); failure != nil {
		return onlineIntent{}, failure
	}
	intentSpec, ok := onlineIntentSpecs[strings.Join(spec.Path, " ")]
	if !ok {
		return onlineIntent{}, nil
	}
	intent := onlineIntent{strings: make(map[string]string), bools: make(map[string]bool), changed: make(map[string]bool)}
	directChanged := false
	for _, flag := range intentSpec.direct {
		changed := command.Flags().Changed(flag.name)
		intent.changed[flag.name] = changed
		if !changed {
			continue
		}
		directChanged = true
		if value := options.direct[flag.name]; value != nil {
			intent.strings[flag.name] = *value
			if len(flag.values) > 0 && !containsString(flag.values, *value) {
				return onlineIntent{}, controlclient.NewInputError("The --" + flag.name + " value is invalid.")
			}
		}
		if value := options.toggles[flag.name]; value != nil {
			intent.bools[flag.name] = *value
		}
	}
	for _, conflict := range intentSpec.conflicts {
		changed := 0
		for _, name := range conflict {
			if intent.changed[name] {
				changed++
			}
		}
		if changed > 1 {
			return onlineIntent{}, controlclient.NewInputError("The --" + strings.Join(conflict, " and --") + " flags conflict.")
		}
	}
	fileChanged := command.Flags().Changed("file")
	if fileChanged && directChanged {
		return onlineIntent{}, controlclient.NewInputError("Choose either direct input flags or --file, not both.")
	}
	if fileChanged {
		if options.file == "-" && options.bearerStdin {
			return onlineIntent{}, controlclient.NewInputError("Standard input cannot provide both command input and the administrator bearer.")
		}
		body, err := controlclient.ReadJSONInput(controlclient.InputOptions{Path: options.file, Stdin: command.InOrStdin(), AllowedMembers: intentSpec.fileMembers})
		if err != nil {
			return onlineIntent{}, controlclient.NewInputError("The command file input is invalid.")
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(body, &object) != nil {
			return onlineIntent{}, controlclient.NewInputError("The command file input is invalid.")
		}
		canonical, err := json.Marshal(object)
		if err != nil {
			return onlineIntent{}, controlclient.NewInputError("The command file input is invalid.")
		}
		intent.body = canonical
		intent.file = true
		validated, err := validatePreparedFileIntent(command, spec, options, intent)
		if err != nil {
			return onlineIntent{}, controlclient.NewInputError("The command file input is invalid.")
		}
		intent.body = validated
		return intent, nil
	}
	if directChanged || intentSpec.defaultDirect {
		for _, flag := range intentSpec.direct {
			if flag.required && !intent.changed[flag.name] {
				return onlineIntent{}, controlclient.NewInputError("The --" + flag.name + " flag is required for direct input.")
			}
		}
		if intentSpec.buildBody != nil {
			body, err := intentSpec.buildBody(intent.strings, intent.bools, intent.changed)
			if err != nil {
				return onlineIntent{}, controlclient.NewInputError("The direct command input is invalid.")
			}
			intent.body = body
			validated, err := validatePreparedFileIntent(command, spec, options, intent)
			if err != nil {
				return onlineIntent{}, controlclient.NewInputError("The direct command input is invalid.")
			}
			intent.body = validated
		}
	}
	return intent, nil
}

func validateOnlineLocalOptions(command *cobra.Command, spec onlineCommandSpec, options *onlineOptions, args []string) *controlclient.OnlineError {
	if _, err := controlclient.New(options.address, controlclient.TransportOptions{}); err != nil {
		return controlclient.NewInputError("The Gateway address is invalid.")
	}
	for index := range requiredPositionals(spec.Use) {
		if index >= len(args) || !gatewayIDPattern.MatchString(args[index]) {
			return controlclient.NewInputError("A command resource ID is invalid.")
		}
	}
	if command.Flags().Changed("idempotency-key") && !validIdempotencyKey(options.idempotencyKey) {
		return controlclient.NewInputError("The idempotency key is invalid.")
	}
	if command.Flags().Changed("limit") && (options.limit < 1 || options.limit > 100) {
		return controlclient.NewInputError("The page limit is invalid.")
	}
	if !command.Flags().Changed("etag") {
		return nil
	}
	path := strings.Join(spec.Path, " ")
	var parts []string
	switch {
	case strings.HasPrefix(path, "server "):
		parts = serverETagPattern.FindStringSubmatch(options.etag)
	case strings.HasPrefix(path, "principal "):
		parts = principalETagPattern.FindStringSubmatch(options.etag)
	case strings.HasPrefix(path, "grant-request "):
		parts = grantRequestETagPattern.FindStringSubmatch(options.etag)
	default:
		return controlclient.NewInputError("The ETag is not valid for this command.")
	}
	if len(parts) != 3 || len(args) == 0 || parts[1] != args[0] {
		return controlclient.NewInputError("The ETag is invalid or belongs to another resource.")
	}
	return nil
}

func validatePreparedFileIntent(command *cobra.Command, spec onlineCommandSpec, options *onlineOptions, intent onlineIntent) ([]byte, error) {
	prepared := *options
	prepared.intent = intent
	switch strings.Join(spec.Path, " ") {
	case "admin credential create":
		return readAdminCredentialCreateInput(command, &prepared)
	case "server create":
		body, _, err := readServerMutationInput(command, &prepared, true)
		return body, err
	case "server update":
		body, _, err := readServerMutationInput(command, &prepared, false)
		return body, err
	case "server operation start":
		body, err := readOnlineJSONInput(command, &prepared, []string{"kind"})
		if err != nil {
			return nil, err
		}
		var input contract.ServerOperationCreate
		if controlclient.DecodeResponse(body, &input) != nil {
			return nil, controlclient.ErrInvalidInput
		}
		if _, err := contract.ParseExplicitServerOperationKind(string(input.Kind)); err != nil {
			return nil, controlclient.ErrInvalidInput
		}
		return json.Marshal(input)
	case "server credential replace":
		body, err := readOnlineJSONInput(command, &prepared, []string{"kind", "expected_revision", "values", "client_secret"})
		if err != nil {
			return nil, err
		}
		_, canonical, err := validateCredentialReplacementInput(body)
		return canonical, err
	case "principal create":
		body, members, err := readPrincipalInput(command, &prepared, true)
		if err != nil || !members["display_name"] || !members["visibility"] || len(members) != 2 {
			return nil, controlclient.ErrInvalidInput
		}
		return body, nil
	case "principal update":
		body, members, err := readPrincipalInput(command, &prepared, false)
		if err != nil || len(members) == 0 {
			return nil, controlclient.ErrInvalidInput
		}
		return body, nil
	case "grant create":
		return readGrantCreateInput(command, &prepared)
	case "grant-request approve":
		body, _, err := readGrantRequestApproval(command, &prepared)
		return body, err
	case "grant-request reject":
		body, _, err := readGrantRequestRejection(command, &prepared)
		return body, err
	default:
		return intent.body, nil
	}
}

func readOnlineJSONInput(command *cobra.Command, options *onlineOptions, allowedMembers []string) ([]byte, error) {
	if options != nil && options.intent.body != nil {
		return append([]byte(nil), options.intent.body...), nil
	}
	return controlclient.ReadJSONInput(controlclient.InputOptions{Path: options.file, Stdin: command.InOrStdin(), AllowedMembers: allowedMembers})
}

func onlineIntentFlag(spec onlineCommandSpec, name string) (bool, bool) {
	intentSpec, ok := onlineIntentSpecs[strings.Join(spec.Path, " ")]
	if !ok {
		return false, false
	}
	for _, flag := range intentSpec.direct {
		if flag.name == name {
			return true, flag.toggle
		}
	}
	return false, false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func marshalIntent(value any) ([]byte, error) {
	return json.Marshal(value)
}

var onlineIntentSpecs = map[string]onlineIntentSpec{
	"admin credential create": {
		direct:        []onlineDirectFlag{{name: "expires-at"}},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, changed map[string]bool) ([]byte, error) {
			if !changed["expires-at"] {
				return []byte(`{"expires_at":null}`), nil
			}
			return marshalIntent(map[string]any{"expires_at": values["expires-at"]})
		},
	},
	"server create": {fileMembers: []string{"namespace", "display_name", "enabled", "transport"}},
	"server update": {
		fileMembers:   []string{"display_name", "enabled", "transport"},
		direct:        []onlineDirectFlag{{name: "display-name"}, {name: "enable", toggle: true}, {name: "disable", toggle: true}},
		conflicts:     [][]string{{"enable", "disable"}},
		defaultDirect: true,
		buildBody: func(values map[string]string, toggles map[string]bool, changed map[string]bool) ([]byte, error) {
			body := make(map[string]any)
			if changed["display-name"] {
				body["display_name"] = values["display-name"]
			}
			if changed["enable"] {
				body["enabled"] = toggles["enable"]
			}
			if changed["disable"] {
				body["enabled"] = !toggles["disable"]
			}
			return marshalIntent(body)
		},
	},
	"server operation start": {
		direct:        []onlineDirectFlag{{name: "kind", values: []string{"reload", "retry", "refresh_catalog", "disconnect_credentials"}, required: true}},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, _ map[string]bool) ([]byte, error) {
			return marshalIntent(map[string]any{"kind": values["kind"]})
		},
	},
	"server credential replace": {fileMembers: []string{"kind", "expected_revision", "values", "client_secret"}},
	"principal create": {
		direct:        []onlineDirectFlag{{name: "display-name", required: true}, {name: "visibility", values: []string{"requestable", "allowed-only", "all"}, required: true}},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, _ map[string]bool) ([]byte, error) {
			return marshalIntent(map[string]any{"display_name": values["display-name"], "visibility": values["visibility"]})
		},
	},
	"principal update": {
		direct: []onlineDirectFlag{
			{name: "display-name"}, {name: "visibility", values: []string{"requestable", "allowed-only", "all"}}, {name: "state", values: []string{"active", "disabled"}},
		},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, changed map[string]bool) ([]byte, error) {
			body := make(map[string]any)
			for flag, member := range map[string]string{"display-name": "display_name", "visibility": "visibility", "state": "state"} {
				if changed[flag] {
					body[member] = values[flag]
				}
			}
			return marshalIntent(body)
		},
	},
	"grant create": {
		fileMembers: []string{"name", "principal_id", "effect", "server_id", "upstream_name", "constraint", "expires_at"},
		direct: []onlineDirectFlag{
			{name: "name", required: true}, {name: "principal-id", required: true}, {name: "effect", values: []string{"allow", "deny"}, required: true}, {name: "server-id", required: true}, {name: "upstream-name"}, {name: "expires-at"},
		},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, changed map[string]bool) ([]byte, error) {
			body := map[string]any{"name": values["name"], "principal_id": values["principal-id"], "effect": values["effect"], "server_id": values["server-id"], "upstream_name": nil, "constraint": nil, "expires_at": nil}
			for _, flag := range []string{"upstream-name", "expires-at"} {
				if changed[flag] {
					body[strings.ReplaceAll(flag, "-", "_")] = values[flag]
				}
			}
			return marshalIntent(body)
		},
	},
	"grant-request approve": {
		fileMembers: []string{"name", "approved_policy"},
		direct: []onlineDirectFlag{
			{name: "name", required: true}, {name: "scope", values: []string{"tool", "server"}, required: true}, {name: "target", required: true}, {name: "duration-seconds"}, {name: "acknowledge-future-tools", toggle: true},
		},
		defaultDirect: true,
		buildBody: func(values map[string]string, toggles map[string]bool, changed map[string]bool) ([]byte, error) {
			policy := map[string]any{"scope": values["scope"], "target": values["target"], "constraint": nil, "duration_seconds": nil, "future_tools_acknowledged": false}
			if changed["duration-seconds"] {
				policy["duration_seconds"] = values["duration-seconds"]
			}
			if changed["acknowledge-future-tools"] {
				policy["future_tools_acknowledged"] = toggles["acknowledge-future-tools"]
			}
			return marshalIntent(map[string]any{"name": values["name"], "approved_policy": policy})
		},
	},
	"grant-request reject": {
		direct:        []onlineDirectFlag{{name: "reason", values: []string{"not_approved", "existing_access", "scope_too_broad", "policy_conflict"}, required: true}},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, _ map[string]bool) ([]byte, error) {
			return marshalIntent(map[string]any{"reason": values["reason"]})
		},
	},
}
