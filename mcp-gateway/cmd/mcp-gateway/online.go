package main

import (
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/spf13/cobra"
)

type onlineCommandSpec struct {
	Path          []string
	Use           string
	ManifestUse   string
	Short         string
	Flags         []string
	RequiredFlags []string
}

type onlineAdminBearer struct {
	value string
	path  string
}

type onlineOptions struct {
	intent         onlineIntent
	address        string
	bearerFile     string
	bearerStdin    bool
	adminBearer    onlineAdminBearer
	output         string
	jsonOutput     bool
	file           string
	etag           string
	idempotencyKey string
	secretOutput   string
	cursor         string
	limit          int
	yes            bool
	open           bool
	direct         map[string]*string
	toggles        map[string]*bool
	filters        map[string]*string
}

func newOnlineCommands() []*cobra.Command {
	groups := make(map[string]*cobra.Command)
	roots := make(map[string]*cobra.Command)
	for _, spec := range onlineCommandSpecs() {
		parentPath := spec.Path[:len(spec.Path)-1]
		var parent *cobra.Command
		for index, name := range parentPath {
			key := strings.Join(parentPath[:index+1], " ")
			group := groups[key]
			if group == nil {
				group = &cobra.Command{Use: name, Short: onlineGroupDescriptions[key]}
				groups[key] = group
				if parent == nil {
					roots[name] = group
				} else {
					parent.AddCommand(group)
				}
			}
			parent = group
		}
		leaf := newOnlineLeaf(spec)
		if parent == nil {
			roots[spec.Path[0]] = leaf
		} else {
			parent.AddCommand(leaf)
		}
	}
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	commands := make([]*cobra.Command, 0, len(names))
	for _, name := range names {
		commands = append(commands, roots[name])
	}
	return commands
}

func newOnlineLeaf(spec onlineCommandSpec) *cobra.Command {
	options := &onlineOptions{direct: make(map[string]*string), toggles: make(map[string]*bool), filters: make(map[string]*string)}
	command := &cobra.Command{
		Use:     spec.Use,
		Short:   spec.Short,
		Example: "mcp-gateway " + spec.ManifestUse,
		Args: func(command *cobra.Command, args []string) error {
			positionals := requiredPositionals(spec.Use)
			if len(args) != len(positionals) {
				mode := selectedOutputMode(command, options.output, options.jsonOutput)
				title := "This command does not accept additional positional arguments."
				if len(args) < len(positionals) {
					name := strings.ToLower(strings.ReplaceAll(positionals[len(args)], "_", " "))
					title = "The " + name + " argument is required."
				}
				return writeOnlineFailure(command, string(mode), onlineUsageProblem(spec, title))
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			resolved, err := resolveExecutionOptions(executionOptionInput{
				Output: options.output, OutputSet: command.Flags().Changed("output"), JSON: options.jsonOutput,
			})
			if err != nil {
				return writeOnlineFailure(command, string(controlclient.OutputHuman), onlineUsageProblem(spec, "Choose either --output human or --output json; --json is the JSON shorthand."))
			}
			options.output = string(resolved.Output)
			intent, failure := prepareOnlineIntent(command, spec, options, args)
			if failure != nil {
				return writeOnlineFailure(command, options.output, failure)
			}
			options.intent = intent
			for _, required := range spec.RequiredFlags {
				if !command.Flags().Changed(required) {
					return writeOnlineFailure(command, options.output, onlineUsageProblem(spec, "The --"+required+" flag is required."))
				}
			}
			selected, failure := acquireOnlineAdminBearer(command, options)
			if failure != nil {
				return writeOnlineFailure(command, options.output, failure)
			}
			options.adminBearer = selected
			return runOnlineCommand(command, spec, options, args)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.address, "address", controlclient.DefaultAddress, "canonical numeric 127/8 Gateway URL")
	flags.StringVar(&options.bearerFile, "admin-bearer-file", "", "owner-readable admin bearer file")
	flags.BoolVar(&options.bearerStdin, "admin-bearer-stdin", false, "read the admin bearer from standard input")
	flags.StringVar(&options.output, "output", string(controlclient.OutputHuman), "output mode: human or json")
	flags.BoolVar(&options.jsonOutput, "json", false, "shorthand for --output json")
	for _, flag := range spec.Flags {
		if direct, toggle := onlineIntentFlag(spec, flag); direct {
			if toggle {
				value := false
				options.toggles[flag] = &value
				flags.BoolVar(options.toggles[flag], flag, false, "direct command input")
			} else {
				value := ""
				options.direct[flag] = &value
				flags.StringVar(options.direct[flag], flag, "", "direct command input")
			}
			continue
		}
		switch flag {
		case "file":
			flags.StringVar(&options.file, flag, "", "one strict JSON input document path or -")
		case "etag":
			flags.StringVar(&options.etag, flag, "", "exact current strong ETag")
		case "idempotency-key":
			flags.StringVar(&options.idempotencyKey, flag, "", "explicit idempotency key")
		case "secret-output":
			flags.StringVar(&options.secretOutput, flag, "", "new owner-only secret output path")
		case "cursor":
			flags.StringVar(&options.cursor, flag, "", "opaque page cursor")
		case "limit":
			flags.IntVar(&options.limit, flag, 0, "maximum rows in this one page")
		case "yes":
			flags.BoolVar(&options.yes, flag, false, "confirm the described consequence noninteractively")
		case "open":
			flags.BoolVar(&options.open, flag, false, "open the one-time authorization URL")
		default:
			value := ""
			options.filters[flag] = &value
			flags.StringVar(options.filters[flag], flag, "", "exact API-supported list filter")
		}
	}
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		mode := selectedOutputMode(command, options.output, options.jsonOutput)
		return writeOnlineFailure(command, string(mode), onlineUsageProblem(spec, "A command flag is invalid or incomplete."))
	})
	return command
}

func acquireOnlineAdminBearer(command *cobra.Command, options *onlineOptions) (onlineAdminBearer, *controlclient.OnlineError) {
	if command == nil || options == nil {
		return onlineAdminBearer{}, controlclient.NewInputError("The administrator bearer selection is invalid.")
	}
	selectedPath := options.bearerFile
	if selectedPath == "" && !options.bearerStdin {
		dataDir, err := command.Root().PersistentFlags().GetString("data-dir")
		if err != nil {
			return onlineAdminBearer{}, controlclient.NewInputError("The selected data directory is invalid.")
		}
		layout, err := gatewaypaths.Resolve(dataDir)
		if err != nil {
			return onlineAdminBearer{}, controlclient.NewInputError("The selected data directory is invalid.")
		}
		selectedPath = layout.AdminBearer
	}
	bearer, err := controlclient.AcquireAdminBearer(controlclient.BearerOptions{
		FilePath: selectedPath, ReadStdin: options.bearerStdin, Stdin: command.InOrStdin(), InputFilePath: options.file,
	})
	if err != nil {
		return onlineAdminBearer{}, controlclient.ProjectBearerProblem(err, selectedPath)
	}
	return onlineAdminBearer{value: bearer, path: selectedPath}, nil
}

func evaluateOnlineResponse(response controlclient.Response, bearerPath string) *controlclient.OnlineError {
	failure := controlclient.EvaluateResponse(response)
	if failure == nil || failure.Status == nil || *failure.Status != 401 {
		return failure
	}
	return controlclient.ProjectBearerProblem(failure, bearerPath)
}

func prepareOnlineSensitiveAction(options *onlineOptions, consequence string, prompt controlclient.ConfirmationPrompt, openTerminal func() (io.WriteCloser, error)) (*controlclient.PreparedSink, *controlclient.OnlineError) {
	if options == nil {
		return nil, controlclient.NewInputError("The command input is invalid.")
	}
	if err := controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: options.yes, Consequence: consequence, Prompt: prompt}); err != nil {
		return nil, controlclient.ClassifyClientError(err)
	}
	sink, err := controlclient.PrepareSensitiveSink(controlclient.SinkOptions{Path: options.secretOutput, OpenTerminal: openTerminal})
	if err != nil {
		return nil, controlclient.ClassifyClientError(err)
	}
	return sink, nil
}

func writeOnlineFailure(command *cobra.Command, rawMode string, failure *controlclient.OnlineError) error {
	failure = projectOnlineFailure(command, failure)
	mode, err := controlclient.ParseOutputMode(rawMode)
	if err != nil {
		mode = controlclient.OutputTable
	}
	if err := controlclient.WriteFailure(command.ErrOrStderr(), mode, failure); err != nil {
		return controlclient.NewInputError("The command error could not be written.")
	}
	return failure
}

func projectOnlineFailure(command *cobra.Command, failure *controlclient.OnlineError) *controlclient.OnlineError {
	if command == nil || failure == nil || failure.Code != "gateway_not_running" {
		return failure
	}
	address, err := command.Flags().GetString("address")
	if err != nil {
		return failure
	}
	dataDir := selectedDataDir(command, "")
	includeDataDir := dataDir != "" && command.Root().PersistentFlags().Changed("data-dir")
	startCommand, err := renderOnlineServeCommand(address, dataDir, includeDataDir)
	projected := *failure
	if err != nil {
		projected.Title = "MCP Gateway is not running. Run mcp-gateway serve with the selected address and data directory."
		return &projected
	}
	projected.Title = "MCP Gateway is not running. Start it with: " + startCommand + "."
	return &projected
}

func commandExitCode(err error) int {
	var online interface{ ExitCode() int }
	if errors.As(err, &online) {
		code := online.ExitCode()
		if code >= 2 && code <= 10 {
			return code
		}
	}
	return 1
}

func onlineCapabilityUses() []string {
	specs := onlineCommandSpecs()
	uses := make([]string, 0, len(specs))
	for _, spec := range specs {
		uses = append(uses, spec.ManifestUse)
	}
	return uses
}

func requiredPositionals(use string) []string {
	fields := strings.Fields(use)
	positionals := make([]string, 0)
	for _, field := range fields[1:] {
		if field == strings.ToUpper(field) {
			positionals = append(positionals, field)
		}
	}
	return positionals
}

func onlineUsageProblem(spec onlineCommandSpec, title string) *controlclient.OnlineError {
	return controlclient.NewInputError(title + " Usage: mcp-gateway " + spec.ManifestUse)
}

func onlineCommandSpecs() []onlineCommandSpec {
	return []onlineCommandSpec{
		onlineSpec([]string{"status"}, "status", "status"),
		onlineSpec([]string{"admin", "credential", "list"}, "list", "admin credential list", "limit", "cursor"),
		onlineSpec([]string{"admin", "credential", "get"}, "get ID", "admin credential get ID"),
		onlineSpec([]string{"admin", "credential", "create"}, "create", "admin credential create [--expires-at RFC3339] [--secret-output NEW_PATH]", "expires-at", "secret-output"),
		onlineSpec([]string{"admin", "credential", "revoke"}, "revoke ID", "admin credential revoke ID", "yes"),
		onlineSpec([]string{"backup", "list"}, "list", "backup list", "limit", "cursor"),
		onlineSpec([]string{"backup", "get"}, "get BACKUP_ID", "backup get BACKUP_ID"),
		onlineSpec([]string{"backup", "create"}, "create", "backup create", "idempotency-key"),
		onlineSpec([]string{"backup", "delete"}, "delete BACKUP_ID", "backup delete BACKUP_ID", "yes"),
		onlineSpec([]string{"server", "list"}, "list", "server list", "limit", "cursor"),
		onlineSpec([]string{"server", "get"}, "get ID", "server get ID"),
		onlineSpec([]string{"server", "create"}, "create", "server create --file PATH", "file", "idempotency-key"),
		onlineSpec([]string{"server", "update"}, "update ID", "server update ID --etag ETAG --file PATH", "etag", "file", "display-name", "enable", "disable", "yes"),
		onlineSpec([]string{"server", "delete"}, "delete ID", "server delete ID --etag ETAG", "etag", "yes"),
		onlineSpec([]string{"server", "operation", "list"}, "list ID", "server operation list ID", "limit", "cursor"),
		onlineSpec([]string{"server", "operation", "get"}, "get ID OPERATION_ID", "server operation get ID OPERATION_ID"),
		onlineSpec([]string{"server", "operation", "start"}, "start ID", "server operation start ID --etag ETAG --file PATH", "etag", "file", "kind", "idempotency-key", "yes"),
		onlineSpec([]string{"server", "credential", "replace"}, "replace ID", "server credential replace ID --etag ETAG --file PATH", "etag", "file", "yes"),
		onlineSpec([]string{"server", "auth-flow", "list"}, "list ID", "server auth-flow list ID", "limit", "cursor"),
		onlineSpec([]string{"server", "auth-flow", "get"}, "get ID FLOW_ID", "server auth-flow get ID FLOW_ID"),
		onlineSpec([]string{"server", "auth-flow", "start"}, "start ID", "server auth-flow start ID --etag ETAG [--open]", "etag", "open"),
		onlineSpec([]string{"server", "auth-flow", "cancel"}, "cancel ID FLOW_ID", "server auth-flow cancel ID FLOW_ID", "yes"),
		onlineSpec([]string{"server", "descriptor", "list"}, "list ID", "server descriptor list ID", "limit", "cursor", "retired"),
		onlineSpec([]string{"server", "descriptor", "get"}, "get ID TOOL_ID", "server descriptor get ID TOOL_ID"),
		onlineSpec([]string{"catalog", "list"}, "list", "catalog list", "limit", "cursor"),
		onlineSpec([]string{"principal", "list"}, "list", "principal list", "limit", "cursor"),
		onlineSpec([]string{"principal", "get"}, "get ID", "principal get ID"),
		onlineSpec([]string{"principal", "create"}, "create", "principal create --display-name NAME --visibility VISIBILITY", "display-name", "visibility"),
		onlineSpec([]string{"principal", "update"}, "update ID", "principal update ID --etag ETAG [--display-name NAME] [--visibility VISIBILITY] [--state STATE]", "etag", "display-name", "visibility", "state", "yes"),
		onlineSpec([]string{"principal", "credential", "issue"}, "issue ID", "principal credential issue ID --etag ETAG [--secret-output NEW_PATH]", "etag", "secret-output", "yes"),
		onlineSpec([]string{"principal", "credential", "revoke"}, "revoke ID", "principal credential revoke ID --etag ETAG", "etag", "yes"),
		onlineSpec([]string{"grant", "list"}, "list", "grant list", "limit", "cursor", "principal-id", "server-id"),
		onlineSpec([]string{"grant", "get"}, "get ID", "grant get ID"),
		onlineSpec([]string{"grant", "create"}, "create", "grant create --file PATH", "file", "principal-id", "effect", "server-id", "upstream-name", "expires-at"),
		onlineSpec([]string{"grant", "delete"}, "delete ID", "grant delete ID", "yes"),
		onlineSpec([]string{"grant-request", "list"}, "list", "grant-request list", "limit", "cursor", "principal-id", "state"),
		onlineSpec([]string{"grant-request", "get"}, "get REQUEST_ID", "grant-request get REQUEST_ID"),
		onlineSpec([]string{"grant-request", "approve"}, "approve REQUEST_ID", "grant-request approve REQUEST_ID --etag ETAG --file PATH", "etag", "file", "scope", "target", "duration-seconds", "acknowledge-future-tools", "yes"),
		onlineSpec([]string{"grant-request", "reject"}, "reject REQUEST_ID", "grant-request reject REQUEST_ID --etag ETAG --file PATH", "etag", "file", "reason", "yes"),
		onlineSpec([]string{"invocation", "list"}, "list", "invocation list", "limit", "cursor", "principal-id", "server-id", "requested-name", "admission-class", "decision", "outcome"),
		onlineSpec([]string{"invocation", "get"}, "get INVOCATION_ID", "invocation get INVOCATION_ID"),
	}
}

func onlineSpec(path []string, use, manifestUse string, flags ...string) onlineCommandSpec {
	return onlineCommandSpec{
		Path: path, Use: use, ManifestUse: manifestUse, Short: onlineLeafDescriptions[manifestUse], Flags: flags,
		RequiredFlags: append([]string(nil), onlineRequiredFlags[manifestUse]...),
	}
}

var onlineGroupDescriptions = map[string]string{
	"admin":                "Manage administrator authority",
	"admin credential":     "Manage administrator credentials",
	"backup":               "Create and manage recovery backups",
	"server":               "Manage upstream MCP server configurations",
	"server operation":     "Inspect and request server operations",
	"server credential":    "Replace server credentials",
	"server auth-flow":     "Manage server OAuth authorization flows",
	"server descriptor":    "Inspect discovered server tools",
	"catalog":              "Inspect published Gateway tools",
	"principal":            "Manage agent principals",
	"principal credential": "Issue and revoke agent credentials",
	"grant":                "Manage agent authorization grants",
	"grant-request":        "Review agent grant requests",
	"invocation":           "Inspect governed tool invocations",
}

//nolint:gosec // Static help text names credential commands but contains no credentials.
var onlineLeafDescriptions = map[string]string{
	"status":                  "Show Gateway status",
	"admin credential list":   "List administrator credentials",
	"admin credential get ID": "Show an administrator credential",
	"admin credential create [--expires-at RFC3339] [--secret-output NEW_PATH]": "Create an administrator credential",
	"admin credential revoke ID":                                   "Revoke an administrator credential",
	"backup list":                                                  "List recovery backups",
	"backup get BACKUP_ID":                                         "Show a recovery backup",
	"backup create":                                                "Create a recovery backup",
	"backup delete BACKUP_ID":                                      "Delete a recovery backup",
	"server list":                                                  "List configured MCP servers",
	"server get ID":                                                "Show a configured MCP server",
	"server create --file PATH":                                    "Create an MCP server configuration",
	"server update ID --etag ETAG --file PATH":                     "Update an MCP server configuration",
	"server delete ID --etag ETAG":                                 "Delete an MCP server configuration",
	"server operation list ID":                                     "List operations for a server",
	"server operation get ID OPERATION_ID":                         "Show a server operation",
	"server operation start ID --etag ETAG --file PATH":            "Request a server operation",
	"server credential replace ID --etag ETAG --file PATH":         "Replace a server credential",
	"server auth-flow list ID":                                     "List OAuth flows for a server",
	"server auth-flow get ID FLOW_ID":                              "Show a server OAuth flow",
	"server auth-flow start ID --etag ETAG [--open]":               "Start server OAuth authorization",
	"server auth-flow cancel ID FLOW_ID":                           "Cancel server OAuth authorization",
	"server descriptor list ID":                                    "List discovered tools for a server",
	"server descriptor get ID TOOL_ID":                             "Show a discovered server tool",
	"catalog list":                                                 "List published Gateway tools",
	"principal list":                                               "List agent principals",
	"principal get ID":                                             "Show an agent principal",
	"principal create --display-name NAME --visibility VISIBILITY": "Create an agent principal",
	"principal update ID --etag ETAG [--display-name NAME] [--visibility VISIBILITY] [--state STATE]": "Update an agent principal",
	"principal credential issue ID --etag ETAG [--secret-output NEW_PATH]":                            "Issue or replace an agent credential",
	"principal credential revoke ID --etag ETAG":                                                      "Revoke an agent credential",
	"grant list":                   "List authorization grants",
	"grant get ID":                 "Show an authorization grant",
	"grant create --file PATH":     "Create an authorization grant",
	"grant delete ID":              "Delete an authorization grant",
	"grant-request list":           "List agent grant requests",
	"grant-request get REQUEST_ID": "Show an agent grant request",
	"grant-request approve REQUEST_ID --etag ETAG --file PATH": "Approve an agent grant request",
	"grant-request reject REQUEST_ID --etag ETAG --file PATH":  "Reject an agent grant request",
	"invocation list":              "List governed tool invocations",
	"invocation get INVOCATION_ID": "Show a governed tool invocation",
}

var onlineRequiredFlags = map[string][]string{
	"server create --file PATH":                                                                       {"file"},
	"server update ID --etag ETAG --file PATH":                                                        {"etag", "file"},
	"server delete ID --etag ETAG":                                                                    {"etag"},
	"server operation start ID --etag ETAG --file PATH":                                               {"etag", "file"},
	"server credential replace ID --etag ETAG --file PATH":                                            {"etag", "file"},
	"server auth-flow start ID --etag ETAG [--open]":                                                  {"etag"},
	"principal update ID --etag ETAG [--display-name NAME] [--visibility VISIBILITY] [--state STATE]": {"etag"},
	"principal credential issue ID --etag ETAG [--secret-output NEW_PATH]":                            {"etag"},
	"principal credential revoke ID --etag ETAG":                                                      {"etag"},
	"grant create --file PATH":                                                                        {"file"},
	"grant-request approve REQUEST_ID --etag ETAG --file PATH":                                        {"etag", "file"},
	"grant-request reject REQUEST_ID --etag ETAG --file PATH":                                         {"etag", "file"},
}
