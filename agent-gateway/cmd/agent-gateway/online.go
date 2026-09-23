package main

import (
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
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
				configureNamespaceCommand(group)
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

func configureNamespaceCommand(command *cobra.Command) {
	command.Args = func(command *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		return writeOnlineFailure(command, string(controlclient.OutputHuman), namespaceUsageProblem(command, "The command is not recognized."))
	}
	command.RunE = func(command *cobra.Command, _ []string) error {
		return command.Help()
	}
	command.SetFlagErrorFunc(func(command *cobra.Command, _ error) error {
		return writeOnlineFailure(command, string(controlclient.OutputHuman), namespaceUsageProblem(command, "A command flag is invalid or incomplete."))
	})
}

func namespaceUsageProblem(command *cobra.Command, title string) *controlclient.OnlineError {
	path := "agent-gateway" + strings.TrimPrefix(command.CommandPath(), command.Root().Name())
	return controlclient.NewInputError(title + " Usage: " + path + " --help")
}

func newOnlineLeaf(spec onlineCommandSpec) *cobra.Command {
	options := &onlineOptions{direct: make(map[string]*string), toggles: make(map[string]*bool), filters: make(map[string]*string)}
	command := &cobra.Command{
		Use:     spec.Use,
		Short:   spec.Short,
		Long:    onlineLongDescription(spec),
		Example: "agent-gateway " + spec.ManifestUse,
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
			defer clear(options.intent.body)
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
	flags.StringVar(&options.address, "address", controlclient.DefaultAddress, "HTTP Gateway URL with port: numeric 127/8 or an explicitly trusted local forwarding hostname")
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
				usage := "direct command input"
				if flag == "visibility" {
					usage = "MCP discovery visibility: requestable, allowed-only, or all; grants no access, MCP grants remain authoritative"
				}
				flags.StringVar(options.direct[flag], flag, "", usage)
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
			flags.StringVar(&options.secretOutput, flag, "", secretOutputFlagUsage(spec))
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

func onlineLongDescription(spec onlineCommandSpec) string {
	switch strings.Join(spec.Path, " ") {
	case "principal create":
		return spec.Short + ". Principals and their singular credential slot remain shared administration. Creation also adds Default Gateway access: an ordinary grant for Gateway's six fixed MCP self-service tools, not downstream tools or future protocols. Human output shows principal metadata; JSON retains principal and default_grant. Issue the credential separately; configure downstream MCP grants separately. Discovery visibility grants no access; MCP grants remain authoritative."
	case "principal update":
		return spec.Short + ". Principals and credentials remain shared administration. --visibility changes MCP discovery only and grants no access; MCP grants remain authoritative. Disabling clears the current credential and invalidates admitted authority; re-enabling restores neither credentials nor deleted grants."
	case "mcp grant create", "mcp grant-request approve":
		return spec.Short + ". --read-only restricts server ALLOW access to current and future tools explicitly declaring readOnlyHint=true. Hints are trusted server declarations, not side-effect isolation. Other ALLOW grants may authorize writes; matching DENY still wins. Read-only requests must retain --read-only and server scope; --acknowledge-future-tools remains required for server approval. Approval reads the submitted restriction before mutation, even with an explicit ETag, and never refreshes that ETag or replays a mutation. Direct flags and --file are mutually exclusive."
	case "audit list", "audit get":
		return spec.Short + ". Filters are authoritative and conjunctive. --from and --until must occur together as UTC timestamps with nine fractional digits, at most 366 days apart. --credential-id matches the operator or known system initiator, not a named human. Continue pages with the same filters and --generation. On stale_cursor discard the traversal and restart; on audit_history_replaced discard prior-history state and restart without the old generation. Restore can discard newer local events. See docs/operators/administration.md."
	case "mcp server create", "mcp server update":
		return spec.Short + ". Strict --file transport.authentication OAuth configuration accepts optional callback_uri (for example http://localhost:3118/callback), auth_server_metadata_url (exact HTTPS metadata location, not issuer identity), and scopes (initial tokens replacing metadata defaults). Omit or use null to restore defaults in a complete transport replacement; [] requests no initial scopes. request_offline_access separately adds advertised offline_access. Exact callback ports must be free; temporary callback-only listeners close when the flow ends. Credentials use only server credential replace. See docs/operators/upstream-servers.md for a complete example."
	case "mcp server auth-flow start":
		return spec.Short + ". A configured callback_uri must exactly match the provider registration. Gateway acquires its loopback callback port before publishing the authorization URL; a collision fails without choosing another port. Stop the conflicting listener and start a new flow. Temporary listeners close on terminal state, expiry, supersession, or shutdown. Main allowed-host settings do not grant callback authority."
	case "admin credential create", "principal credential issue", "principal credential rotate":
		return spec.Short + ". The bearer is published once to a new non-symlink 0600 owner-only file, or to a controlling terminal when --secret-output is omitted. It is never written to stdout or JSON and cannot be recovered after publication."
	case "admin credential rotate":
		return spec.Short + ". --secret-output is required and must name a new non-symlink 0600 owner-only replacement-bearer file. The replacement does not overwrite the default bearer file. The bearer is never written to stdout or JSON and cannot be recovered after publication."
	default:
		return ""
	}
}

func secretOutputFlagUsage(spec onlineCommandSpec) string {
	if strings.Join(spec.Path, " ") == "admin credential rotate" {
		return "required new non-symlink 0600 owner-only replacement-bearer file"
	}
	return "new non-symlink 0600 owner-only bearer file; omit for one-time controlling-terminal display"
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
			return onlineAdminBearer{}, installationSelectionProblem(err)
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
		return nil, credentialSinkPreparationFailure(err, true)
	}
	return sink, nil
}

func credentialSinkPreparationFailure(err error, allowTerminal bool) *controlclient.OnlineError {
	failure := controlclient.ClassifyClientError(err)
	if failure.Code != "client_secret_sink_unavailable" {
		return failure
	}
	failure.Title = "The one-time bearer output could not be prepared. No credential request was submitted. Use a new non-symlink owner-only output path."
	if allowTerminal {
		failure.Title += " Alternatively, omit --secret-output to use a controlling terminal."
	}
	return failure
}

func oneTimeBearerPublicationNote(secretOutput string) string {
	destination := "the controlling terminal"
	if secretOutput != "" {
		destination = "the owner-only file"
	}
	return "Bearer published once to " + destination + "; it is not included in command output and cannot be shown again."
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
	if command != nil && failure != nil && strings.Contains(command.CommandPath(), " audit ") {
		projected := *failure
		switch failure.Code {
		case "audit_history_replaced":
			projected.Title = "Audit history may have been replaced by restore; newer local events may have been discarded. Discard previous-history state and restart audit list without the old cursor or generation."
			return &projected
		case "stale_cursor":
			projected.Title = "The audit traversal expired or history was pruned. Discard previous pages and restart audit list without the cursor, comparing history generations before using earlier records."
			return &projected
		}
	}
	if command == nil || failure == nil || failure.Code != "gateway_not_running" {
		return failure
	}
	address, err := command.Flags().GetString("address")
	if err != nil {
		return failure
	}
	if _, err := controlclient.ListenAuthority(address); err != nil {
		projected := *failure
		projected.Title = "The selected Gateway hostname refused the connection. Check the trusted local forwarding destination and start Gateway on its numeric IPv4 loopback listener with the hostname explicitly allowed."
		return &projected
	}
	dataDir := selectedDataDir(command, "")
	includeDataDir := dataDir != "" && command.Root().PersistentFlags().Changed("data-dir")
	startCommand, err := renderOnlineServeCommand(address, dataDir, includeDataDir)
	projected := *failure
	if err != nil {
		projected.Title = "Agent Gateway is not running. Run agent-gateway serve with the selected address and data directory."
		return &projected
	}
	projected.Title = "Agent Gateway is not running. Start it with: " + startCommand + "."
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
	return controlclient.NewInputError(title + " Usage: agent-gateway " + spec.ManifestUse)
}

func onlineCommandSpecs() []onlineCommandSpec {
	return []onlineCommandSpec{
		onlineSpec([]string{"http", "traffic", "list"}, "list", "http traffic list", "limit", "cursor", "principal-id", "destination", "type", "decision", "outcome"),
		onlineSpec([]string{"http", "traffic", "get"}, "get ID", "http traffic get ID"),
		onlineSpec([]string{"http", "grant", "list"}, "list", "http grant list", "limit", "cursor"),
		onlineSpec([]string{"http", "grant", "get"}, "get ID", "http grant get ID"),
		onlineSpec([]string{"http", "grant", "create"}, "create", "http grant create --file PATH", "file", "yes"),
		onlineSpec([]string{"http", "grant", "update"}, "update ID", "http grant update ID --file PATH [--etag ETAG]", "file", "etag", "yes"),
		onlineSpec([]string{"http", "grant", "delete"}, "delete ID", "http grant delete ID [--etag ETAG]", "etag", "yes"),
		onlineSpec([]string{"http", "default", "get"}, "get ID", "http default get ID"),
		onlineSpec([]string{"http", "default", "update"}, "update ID", "http default update ID --file PATH [--etag ETAG]", "file", "etag", "yes"),
		onlineSpec([]string{"http", "test-access"}, "test-access", "http test-access --file PATH", "file"),
		onlineSpec([]string{"http", "credential", "list"}, "list", "http credential list", "limit", "cursor"),
		onlineSpec([]string{"http", "credential", "get"}, "get ID", "http credential get ID"),
		onlineSpec([]string{"http", "credential", "create"}, "create", "http credential create --file PATH", "file", "yes"),
		onlineSpec([]string{"http", "credential", "update"}, "update ID", "http credential update ID --file PATH [--etag ETAG]", "file", "etag", "yes"),
		onlineSpec([]string{"http", "credential", "rotate"}, "rotate ID", "http credential rotate ID --file PATH [--etag ETAG]", "file", "etag", "yes"),
		onlineSpec([]string{"http", "credential", "delete"}, "delete ID", "http credential delete ID [--etag ETAG]", "etag", "yes"),
		onlineSpec([]string{"status"}, "status", "status"),
		onlineSpec([]string{"audit", "list"}, "list", "audit list", "limit", "cursor", "generation", "actor-type", "credential-id", "category", "action", "target-type", "target-id", "outcome", "correlation-id", "from", "until"),
		onlineSpec([]string{"audit", "get"}, "get AUDIT_EVENT_ID", "audit get AUDIT_EVENT_ID", "generation"),
		onlineSpec([]string{"admin", "credential", "list"}, "list", "admin credential list", "limit", "cursor"),
		onlineSpec([]string{"admin", "credential", "get"}, "get ID", "admin credential get ID"),
		onlineSpec([]string{"admin", "credential", "create"}, "create", "admin credential create [--expires-at RFC3339] [--secret-output NEW_PATH]", "expires-at", "secret-output"),
		onlineSpec([]string{"admin", "credential", "rotate"}, "rotate OLD_CREDENTIAL_ID", "admin credential rotate OLD_CREDENTIAL_ID --secret-output NEW_PATH", "secret-output", "yes"),
		onlineSpec([]string{"admin", "credential", "revoke"}, "revoke ID", "admin credential revoke ID", "yes"),
		onlineSpec([]string{"backup", "list"}, "list", "backup list", "limit", "cursor"),
		onlineSpec([]string{"backup", "get"}, "get BACKUP_ID", "backup get BACKUP_ID"),
		onlineSpec([]string{"backup", "create"}, "create", "backup create", "idempotency-key"),
		onlineSpec([]string{"backup", "delete"}, "delete BACKUP_ID", "backup delete BACKUP_ID", "yes"),
		onlineSpec([]string{"mcp", "server", "list"}, "list", "mcp server list", "limit", "cursor"),
		onlineSpec([]string{"mcp", "server", "get"}, "get ID", "mcp server get ID"),
		onlineSpec([]string{"mcp", "server", "create"}, "create", "mcp server create --file PATH", "file", "idempotency-key"),
		onlineSpec([]string{"mcp", "server", "update"}, "update ID", "mcp server update ID [--etag ETAG] [--display-name NAME] [--enable|--disable] [--file PATH]", "etag", "display-name", "enable", "disable", "file", "yes"),
		onlineSpec([]string{"mcp", "server", "delete"}, "delete ID", "mcp server delete ID [--etag ETAG]", "etag", "yes"),
		onlineSpec([]string{"mcp", "server", "operation", "list"}, "list ID", "mcp server operation list ID", "limit", "cursor"),
		onlineSpec([]string{"mcp", "server", "operation", "get"}, "get ID OPERATION_ID", "mcp server operation get ID OPERATION_ID"),
		onlineSpec([]string{"mcp", "server", "operation", "start"}, "start ID", "mcp server operation start ID --kind KIND [--etag ETAG]", "kind", "etag", "idempotency-key", "yes"),
		onlineSpec([]string{"mcp", "server", "credential", "replace"}, "replace ID", "mcp server credential replace ID --file PATH [--etag ETAG]", "file", "etag", "yes"),
		onlineSpec([]string{"mcp", "server", "auth-flow", "list"}, "list ID", "mcp server auth-flow list ID", "limit", "cursor"),
		onlineSpec([]string{"mcp", "server", "auth-flow", "get"}, "get ID FLOW_ID", "mcp server auth-flow get ID FLOW_ID"),
		onlineSpec([]string{"mcp", "server", "auth-flow", "start"}, "start ID", "mcp server auth-flow start ID [--etag ETAG] [--open]", "etag", "open"),
		onlineSpec([]string{"mcp", "server", "auth-flow", "cancel"}, "cancel ID FLOW_ID", "mcp server auth-flow cancel ID FLOW_ID", "yes"),
		onlineSpec([]string{"mcp", "server", "descriptor", "list"}, "list ID", "mcp server descriptor list ID", "limit", "cursor", "retired"),
		onlineSpec([]string{"mcp", "server", "descriptor", "get"}, "get ID TOOL_ID", "mcp server descriptor get ID TOOL_ID"),
		onlineSpec([]string{"mcp", "catalog", "list"}, "list", "mcp catalog list", "limit", "cursor"),
		onlineSpec([]string{"principal", "list"}, "list", "principal list", "limit", "cursor"),
		onlineSpec([]string{"principal", "get"}, "get ID", "principal get ID"),
		onlineSpec([]string{"principal", "create"}, "create", "principal create --display-name NAME --visibility VISIBILITY", "display-name", "visibility"),
		onlineSpec([]string{"principal", "update"}, "update ID", "principal update ID [--etag ETAG] [--display-name NAME] [--visibility VISIBILITY] [--state STATE] [--http-default POLICY]", "etag", "display-name", "visibility", "state", "http-default", "yes"),
		onlineSpec([]string{"principal", "credential", "issue"}, "issue ID", "principal credential issue ID [--etag ETAG] [--secret-output NEW_PATH]", "etag", "secret-output", "yes"),
		onlineSpec([]string{"principal", "credential", "rotate"}, "rotate ID", "principal credential rotate ID [--etag ETAG] [--secret-output NEW_PATH]", "etag", "secret-output", "yes"),
		onlineSpec([]string{"principal", "credential", "revoke"}, "revoke ID", "principal credential revoke ID [--etag ETAG]", "etag", "yes"),
		onlineSpec([]string{"mcp", "grant", "list"}, "list", "mcp grant list", "limit", "cursor", "principal-id", "server-id"),
		onlineSpec([]string{"mcp", "grant", "get"}, "get ID", "mcp grant get ID"),
		onlineSpec([]string{"mcp", "grant", "create"}, "create", "mcp grant create --principal-id ID --effect EFFECT --server-id ID [--description TEXT] [--upstream-name NAME] [--expires-at RFC3339] [--read-only] [--file PATH]", "description", "principal-id", "effect", "server-id", "upstream-name", "expires-at", "read-only", "file"),
		onlineSpec([]string{"mcp", "grant", "update"}, "update ID", "mcp grant update ID --description TEXT [--etag ETAG]", "description", "etag"),
		onlineSpec([]string{"mcp", "grant", "delete"}, "delete ID", "mcp grant delete ID", "yes"),
		onlineSpec([]string{"mcp", "grant-request", "list"}, "list", "mcp grant-request list", "limit", "cursor", "principal-id", "state"),
		onlineSpec([]string{"mcp", "grant-request", "get"}, "get REQUEST_ID", "mcp grant-request get REQUEST_ID"),
		onlineSpec([]string{"mcp", "grant-request", "approve"}, "approve REQUEST_ID", "mcp grant-request approve REQUEST_ID --scope SCOPE --target TARGET [--description TEXT] [--etag ETAG] [--duration-seconds SECONDS] [--acknowledge-future-tools] [--read-only] [--file PATH]", "description", "scope", "target", "etag", "duration-seconds", "acknowledge-future-tools", "read-only", "file", "yes"),
		onlineSpec([]string{"mcp", "grant-request", "reject"}, "reject REQUEST_ID", "mcp grant-request reject REQUEST_ID --reason REASON [--etag ETAG]", "reason", "etag", "yes"),
		onlineSpec([]string{"mcp", "invocation", "list"}, "list", "mcp invocation list", "limit", "cursor", "principal-id", "server-id", "requested-name", "admission-class", "decision", "outcome"),
		onlineSpec([]string{"mcp", "invocation", "get"}, "get INVOCATION_ID", "mcp invocation get INVOCATION_ID"),
	}
}

func onlineSpec(path []string, use, manifestUse string, flags ...string) onlineCommandSpec {
	return onlineCommandSpec{
		Path: path, Use: use, ManifestUse: manifestUse, Short: onlineLeafDescriptions[manifestUse], Flags: flags,
		RequiredFlags: append([]string(nil), onlineRequiredFlags[manifestUse]...),
	}
}

//nolint:gosec // Static help text names credential commands but contains no credentials.
var onlineGroupDescriptions = map[string]string{
	"http":                  "Manage HTTP access",
	"http traffic":          "Inspect recorded HTTP traffic",
	"http grant":            "Manage HTTP access grants",
	"http default":          "Manage principal HTTP defaults",
	"http credential":       "Manage scoped HTTP credentials",
	"admin":                 "Manage administrator authority",
	"admin credential":      "Manage administrator credentials",
	"backup":                "Create and manage recovery backups",
	"mcp":                   "Manage MCP servers, tools, permissions, and invocation history",
	"mcp server":            "Manage upstream MCP server configurations",
	"mcp server operation":  "Inspect and request server operations",
	"mcp server credential": "Replace server credentials",
	"mcp server auth-flow":  "Manage server OAuth authorization flows",
	"mcp server descriptor": "Inspect discovered server tools",
	"mcp catalog":           "Inspect published Gateway tools",
	"principal":             "Manage agent principals",
	"principal credential":  "Issue, rotate, and revoke agent credentials",
	"mcp grant":             "Manage MCP authorization grants",
	"mcp grant-request":     "Review MCP permission approval requests",
	"mcp invocation":        "Inspect recorded MCP invocations",
	"audit":                 "Inspect retained control-plane audit evidence",
}

//nolint:gosec // Static help text names credential commands but contains no credentials.
var onlineLeafDescriptions = map[string]string{
	"http traffic list":                                   "List recorded HTTP traffic",
	"http traffic get ID":                                 "Inspect admission-time HTTP evidence and terminal uncertainty",
	"http grant list":                                     "List HTTP access grants",
	"http grant get ID":                                   "Inspect an HTTP grant",
	"http grant create --file PATH":                       "Create an HTTP grant",
	"http grant update ID --file PATH [--etag ETAG]":      "Replace HTTP policy atomically",
	"http grant delete ID [--etag ETAG]":                  "Delete an HTTP grant",
	"http default get ID":                                 "Inspect a principal HTTP default",
	"http default update ID --file PATH [--etag ETAG]":    "Patch principal http_default using its unified ETag",
	"http test-access --file PATH":                        "Preview policy only without DNS, dispatch or secret resolution",
	"http credential list":                                "List scoped HTTP credentials without secrets",
	"http credential get ID":                              "Show HTTP credential boundaries, recipe and references",
	"http credential create --file PATH":                  "Create a scoped HTTP credential from a write-only file",
	"http credential update ID --file PATH [--etag ETAG]": "Update HTTP credential metadata and scope",
	"http credential rotate ID --file PATH [--etag ETAG]": "Replace HTTP credential material without revealing stored secrets",
	"http credential delete ID [--etag ETAG]":             "Delete an unreferenced HTTP credential",
	"status":                   "Show Gateway status",
	"audit list":               "List newest-first retained control-plane audit events",
	"audit get AUDIT_EVENT_ID": "Show bounded audit event detail and retention history",
	"admin credential list":    "List administrator credentials",
	"admin credential get ID":  "Look up current administrator credential metadata by ID",
	"admin credential create [--expires-at RFC3339] [--secret-output NEW_PATH]": "Create an administrator credential",
	"admin credential rotate OLD_CREDENTIAL_ID --secret-output NEW_PATH":        "Rotate an administrator credential with durable replacement verification",
	"admin credential revoke ID":    "Revoke an administrator credential",
	"backup list":                   "List recovery backups",
	"backup get BACKUP_ID":          "Look up a recovery backup by ID",
	"backup create":                 "Create a recovery backup",
	"backup delete BACKUP_ID":       "Delete a recovery backup",
	"mcp server list":               "List configured MCP servers",
	"mcp server get ID":             "Show MCP server details and the current mutation ETag",
	"mcp server create --file PATH": "Create an MCP server configuration",
	"mcp server update ID [--etag ETAG] [--display-name NAME] [--enable|--disable] [--file PATH]": "Update an MCP server configuration",
	"mcp server delete ID [--etag ETAG]":                           "Delete an MCP server configuration",
	"mcp server operation list ID":                                 "List operations for a server",
	"mcp server operation get ID OPERATION_ID":                     "Look up current server operation state by ID",
	"mcp server operation start ID --kind KIND [--etag ETAG]":      "Request a server operation",
	"mcp server credential replace ID --file PATH [--etag ETAG]":   "Replace a server credential",
	"mcp server auth-flow list ID":                                 "List OAuth flows for a server",
	"mcp server auth-flow get ID FLOW_ID":                          "Look up current server OAuth flow state by ID",
	"mcp server auth-flow start ID [--etag ETAG] [--open]":         "Start server OAuth authorization",
	"mcp server auth-flow cancel ID FLOW_ID":                       "Cancel server OAuth authorization",
	"mcp server descriptor list ID":                                "List discovered tools for a server",
	"mcp server descriptor get ID TOOL_ID":                         "Look up a discovered server tool by ID",
	"mcp catalog list":                                             "List published Gateway tools",
	"principal list":                                               "List agent principals",
	"principal get ID":                                             "Show agent principal details and the current mutation ETag",
	"principal create --display-name NAME --visibility VISIBILITY": "Create an agent principal",
	"principal update ID [--etag ETAG] [--display-name NAME] [--visibility VISIBILITY] [--state STATE] [--http-default POLICY]": "Atomically update principal settings",
	"principal credential issue ID [--etag ETAG] [--secret-output NEW_PATH]":                                                    "Issue an agent credential into an empty slot",
	"principal credential rotate ID [--etag ETAG] [--secret-output NEW_PATH]":                                                   "Rotate an occupied agent credential atomically",
	"principal credential revoke ID [--etag ETAG]":                                                                              "Revoke an agent credential",
	"mcp grant list":   "List authorization grants",
	"mcp grant get ID": "Look up an authorization grant by ID",
	"mcp grant create --principal-id ID --effect EFFECT --server-id ID [--description TEXT] [--upstream-name NAME] [--expires-at RFC3339] [--read-only] [--file PATH]": "Create an authorization grant",
	"mcp grant update ID --description TEXT [--etag ETAG]": "Update grant display metadata",
	"mcp grant delete ID":              "Delete an authorization grant",
	"mcp grant-request list":           "List agent grant requests",
	"mcp grant-request get REQUEST_ID": "Show grant-request evidence and the current mutation ETag",
	"mcp grant-request approve REQUEST_ID --scope SCOPE --target TARGET [--description TEXT] [--etag ETAG] [--duration-seconds SECONDS] [--acknowledge-future-tools] [--read-only] [--file PATH]": "Approve an agent grant request",
	"mcp grant-request reject REQUEST_ID --reason REASON [--etag ETAG]": "Reject an agent grant request",
	"mcp invocation list":              "List recorded MCP invocations",
	"mcp invocation get INVOCATION_ID": "Show MCP invocation evidence; JSON includes retained redacted arguments",
}

var onlineRequiredFlags = map[string][]string{
	"http grant create --file PATH":                                      {"file"},
	"http grant update ID --file PATH [--etag ETAG]":                     {"file"},
	"http default update ID --file PATH [--etag ETAG]":                   {"file"},
	"http test-access --file PATH":                                       {"file"},
	"http credential create --file PATH":                                 {"file"},
	"http credential update ID --file PATH [--etag ETAG]":                {"file"},
	"http credential rotate ID --file PATH [--etag ETAG]":                {"file"},
	"admin credential rotate OLD_CREDENTIAL_ID --secret-output NEW_PATH": {"secret-output"},
	"mcp server create --file PATH":                                      {"file"},
	"mcp server credential replace ID --file PATH [--etag ETAG]":         {"file"},
}
