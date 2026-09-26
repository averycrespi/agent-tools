package main

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
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
			return onlineIntent{}, preparedIntentError(err, "The command file input is invalid.")
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
				return onlineIntent{}, preparedIntentError(err, "The direct command input is invalid.")
			}
			intent.body = validated
		}
	}
	return intent, nil
}

func preparedIntentError(err error, fallback string) *controlclient.OnlineError {
	var inputError *serverMutationInputError
	if errors.As(err, &inputError) {
		return controlclient.NewServerConfigurationInputError(string(inputError.field), string(inputError.rule))
	}
	var diagnostic *controlclient.OnlineError
	if errors.As(err, &diagnostic) && diagnostic.Code == "client_invalid_input" && diagnostic.Status == nil {
		return controlclient.NewInputError(diagnostic.Title)
	}
	return controlclient.NewInputError(fallback)
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
	maximumPage := 100
	switch strings.Join(spec.Path, " ") {
	case "mcp server list", "mcp server descriptor list", "mcp server operation list", "mcp catalog list":
		maximumPage = 50
	}
	if command.Flags().Changed("limit") && (options.limit < 1 || options.limit > maximumPage) {
		return controlclient.NewInputError("The page limit is invalid.")
	}
	if len(spec.Path) > 0 && spec.Path[0] == "audit" {
		for name, value := range options.filters {
			if command.Flags().Changed(name) && *value == "" {
				return controlclient.NewInputError("Audit filter values must not be empty.")
			}
		}
		if command.Flags().Changed("cursor") && options.cursor == "" {
			return controlclient.NewInputError("The audit cursor must not be empty.")
		}
		if _, err := auditReadPath(options, args); err != nil {
			return controlclient.NewInputError("Audit filters are invalid. Supply from/until together as fixed UTC nanosecond timestamps, at most 366 days apart.")
		}
	}
	if !command.Flags().Changed("etag") {
		return nil
	}
	path := strings.Join(spec.Path, " ")
	var parts []string
	switch {
	case strings.HasPrefix(path, "http credential "):
		parts = httpCredentialETagPattern.FindStringSubmatch(options.etag)
	case strings.HasPrefix(path, "http grant "):
		parts = httpGrantETagPattern.FindStringSubmatch(options.etag)
	case strings.HasPrefix(path, "http default "):
		parts = principalETagPattern.FindStringSubmatch(options.etag)
	case strings.HasPrefix(path, "mcp server "):
		parts = serverETagPattern.FindStringSubmatch(options.etag)
	case strings.HasPrefix(path, "agent "):
		parts = principalETagPattern.FindStringSubmatch(options.etag)
	case strings.HasPrefix(path, "mcp grant "):
		parts = grantETagPattern.FindStringSubmatch(options.etag)
	case strings.HasPrefix(path, "mcp grant-request "):
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
	case "mcp server create":
		body, _, err := readServerMutationInput(command, &prepared, true)
		return body, err
	case "mcp server update":
		body, _, err := readServerMutationInput(command, &prepared, false)
		return body, err
	case "mcp server operation start":
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
	case "mcp server credential replace":
		body, err := readOnlineJSONInput(command, &prepared, []string{"kind", "expected_revision", "values", "client_secret"})
		if err != nil {
			return nil, err
		}
		_, canonical, err := validateCredentialReplacementInput(body)
		return canonical, err
	case "agent create":
		body, members, err := readPrincipalInput(command, &prepared, true)
		if err != nil || !members["display_name"] || !members["visibility"] || len(members) != 2 {
			return nil, controlclient.ErrInvalidInput
		}
		return body, nil
	case "agent update":
		body, members, err := readPrincipalInput(command, &prepared, false)
		if err != nil || len(members) == 0 {
			return nil, controlclient.ErrInvalidInput
		}
		return body, nil
	case "mcp grant create":
		return readGrantCreateInput(command, &prepared)
	case "mcp grant-request approve":
		body, _, err := readGrantRequestApproval(command, &prepared)
		return body, err
	case "mcp grant-request reject":
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
	"http grant create":      {fileMembers: []string{"principal_id", "description", "policy", "expires_at"}},
	"http grant update":      {fileMembers: []string{"principal_id", "description", "policy", "expires_at"}},
	"http default update":    {fileMembers: []string{"http_default"}},
	"http test-access":       {fileMembers: []string{"principal_id", "url", "method", "connect"}},
	"http credential create": {fileMembers: []string{"name", "boundary", "recipe", "secret"}},
	"http credential update": {fileMembers: []string{"name", "boundary", "recipe"}},
	"http credential rotate": {fileMembers: []string{"secret"}},
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
	"mcp server create": {fileMembers: []string{"namespace", "display_name", "enabled", "transport"}},
	"mcp server update": {
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
	"mcp server operation start": {
		direct:        []onlineDirectFlag{{name: "kind", values: []string{"reload", "retry", "refresh_catalog", "disconnect_credentials"}, required: true}},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, _ map[string]bool) ([]byte, error) {
			return marshalIntent(map[string]any{"kind": values["kind"]})
		},
	},
	"mcp server credential replace": {fileMembers: []string{"kind", "expected_revision", "values", "client_secret"}},
	"agent create": {
		direct:        []onlineDirectFlag{{name: "display-name", required: true}, {name: "visibility", values: []string{"requestable", "allowed-only", "all"}, required: true}},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, _ map[string]bool) ([]byte, error) {
			return marshalIntent(map[string]any{"display_name": values["display-name"], "visibility": values["visibility"]})
		},
	},
	"agent update": {
		direct: []onlineDirectFlag{
			{name: "display-name"}, {name: "visibility", values: []string{"requestable", "allowed-only", "all"}}, {name: "state", values: []string{"active", "disabled"}}, {name: "http-default", values: []string{"allow", "block"}},
		},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, changed map[string]bool) ([]byte, error) {
			body := make(map[string]any)
			for flag, member := range map[string]string{"display-name": "display_name", "visibility": "visibility", "state": "state", "http-default": "http_default"} {
				if changed[flag] {
					body[member] = values[flag]
				}
			}
			return marshalIntent(body)
		},
	},
	"mcp grant create": {
		fileMembers: []string{"description", "principal_id", "effect", "server_id", "upstream_name", "constraint", "expires_at", "read_only"},
		direct: []onlineDirectFlag{
			{name: "description"}, {name: "principal-id", required: true}, {name: "effect", values: []string{"allow", "deny"}, required: true}, {name: "server-id", required: true}, {name: "upstream-name"}, {name: "expires-at"}, {name: "read-only", toggle: true},
		},
		defaultDirect: true,
		buildBody: func(values map[string]string, toggles map[string]bool, changed map[string]bool) ([]byte, error) {
			body := map[string]any{"description": nil, "principal_id": values["principal-id"], "effect": values["effect"], "server_id": values["server-id"], "upstream_name": nil, "constraint": nil, "expires_at": nil}
			if changed["read-only"] {
				body["read_only"] = toggles["read-only"]
			}
			if changed["description"] {
				body["description"] = values["description"]
			}
			for _, flag := range []string{"upstream-name", "expires-at"} {
				if changed[flag] {
					body[strings.ReplaceAll(flag, "-", "_")] = values[flag]
				}
			}
			return marshalIntent(body)
		},
	},
	"mcp grant update": {
		direct:        []onlineDirectFlag{{name: "description", required: true}},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, _ map[string]bool) ([]byte, error) {
			var description any = values["description"]
			if values["description"] == "" {
				description = nil
			}
			return marshalIntent(map[string]any{"description": description})
		},
	},
	"mcp grant-request approve": {
		fileMembers: []string{"description", "approved_policy"},
		direct: []onlineDirectFlag{
			{name: "description"}, {name: "scope", values: []string{"tool", "server"}, required: true}, {name: "target", required: true}, {name: "duration-seconds"}, {name: "acknowledge-future-tools", toggle: true}, {name: "read-only", toggle: true},
		},
		defaultDirect: true,
		buildBody: func(values map[string]string, toggles map[string]bool, changed map[string]bool) ([]byte, error) {
			policy := map[string]any{"scope": values["scope"], "target": values["target"], "constraint": nil, "duration_seconds": nil, "future_tools_acknowledged": false}
			if changed["read-only"] {
				policy["read_only"] = toggles["read-only"]
			}
			if changed["duration-seconds"] {
				policy["duration_seconds"] = values["duration-seconds"]
			}
			if changed["acknowledge-future-tools"] {
				policy["future_tools_acknowledged"] = toggles["acknowledge-future-tools"]
			}
			var description any
			if changed["description"] {
				description = values["description"]
			}
			return marshalIntent(map[string]any{"description": description, "approved_policy": policy})
		},
	},
	"mcp grant-request reject": {
		direct:        []onlineDirectFlag{{name: "reason", values: []string{"not_approved", "existing_access", "scope_too_broad", "policy_conflict"}, required: true}},
		defaultDirect: true,
		buildBody: func(values map[string]string, _ map[string]bool, _ map[string]bool) ([]byte, error) {
			return marshalIntent(map[string]any{"reason": values["reason"]})
		},
	},
}
