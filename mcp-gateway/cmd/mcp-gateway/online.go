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
	Path        []string
	Use         string
	ManifestUse string
	Flags       []string
}

type onlineAdminBearer struct {
	value string
	path  string
}

type onlineOptions struct {
	address        string
	bearerFile     string
	bearerStdin    bool
	adminBearer    onlineAdminBearer
	output         string
	file           string
	etag           string
	idempotencyKey string
	secretOutput   string
	cursor         string
	limit          int
	yes            bool
	open           bool
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
				group = &cobra.Command{Use: name, Short: "Online Gateway control commands"}
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
	options := &onlineOptions{filters: make(map[string]*string)}
	command := &cobra.Command{
		Use:   spec.Use,
		Short: "Operate the local Gateway through its public control API",
		Args: func(command *cobra.Command, args []string) error {
			expected := requiredArguments(spec.Use)
			if len(args) != expected {
				return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command arguments are invalid."))
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if _, err := controlclient.ParseOutputMode(options.output); err != nil {
				return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
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
	flags.StringVar(&options.output, "output", string(controlclient.OutputTable), "output mode: table or json")
	for _, flag := range spec.Flags {
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
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command flags are invalid."))
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
	mode, err := controlclient.ParseOutputMode(rawMode)
	if err != nil {
		mode = controlclient.OutputTable
	}
	if err := controlclient.WriteFailure(command.ErrOrStderr(), mode, failure); err != nil {
		return controlclient.NewInputError("The command error could not be written.")
	}
	return failure
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

func requiredArguments(use string) int {
	fields := strings.Fields(use)
	count := 0
	for _, field := range fields[1:] {
		if field == strings.ToUpper(field) {
			count++
		}
	}
	return count
}

func onlineCommandSpecs() []onlineCommandSpec {
	return []onlineCommandSpec{
		onlineSpec([]string{"status"}, "status", "status"),
		onlineSpec([]string{"admin-credential", "list"}, "list", "admin-credential list", "limit", "cursor"),
		onlineSpec([]string{"admin-credential", "get"}, "get ID", "admin-credential get ID"),
		onlineSpec([]string{"admin-credential", "create"}, "create", "admin-credential create --file PATH [--secret-output NEW_PATH]", "file", "secret-output"),
		onlineSpec([]string{"admin-credential", "revoke"}, "revoke ID", "admin-credential revoke ID", "yes"),
		onlineSpec([]string{"backup", "list"}, "list", "backup list", "limit", "cursor"),
		onlineSpec([]string{"backup", "get"}, "get BACKUP_ID", "backup get BACKUP_ID"),
		onlineSpec([]string{"backup", "create"}, "create", "backup create", "idempotency-key"),
		onlineSpec([]string{"backup", "delete"}, "delete BACKUP_ID", "backup delete BACKUP_ID", "yes"),
		onlineSpec([]string{"server", "list"}, "list", "server list", "limit", "cursor"),
		onlineSpec([]string{"server", "get"}, "get ID", "server get ID"),
		onlineSpec([]string{"server", "create"}, "create", "server create --file PATH", "file", "idempotency-key"),
		onlineSpec([]string{"server", "update"}, "update ID", "server update ID --etag ETAG --file PATH", "etag", "file", "yes"),
		onlineSpec([]string{"server", "delete"}, "delete ID", "server delete ID --etag ETAG", "etag", "yes"),
		onlineSpec([]string{"server", "operation", "list"}, "list ID", "server operation list ID", "limit", "cursor"),
		onlineSpec([]string{"server", "operation", "get"}, "get ID OPERATION_ID", "server operation get ID OPERATION_ID"),
		onlineSpec([]string{"server", "operation", "start"}, "start ID", "server operation start ID --etag ETAG --file PATH", "etag", "file", "idempotency-key", "yes"),
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
		onlineSpec([]string{"principal", "create"}, "create", "principal create --file PATH", "file"),
		onlineSpec([]string{"principal", "update"}, "update ID", "principal update ID --etag ETAG --file PATH", "etag", "file", "yes"),
		onlineSpec([]string{"principal", "credential", "issue"}, "issue ID", "principal credential issue ID --etag ETAG [--secret-output NEW_PATH]", "etag", "secret-output", "yes"),
		onlineSpec([]string{"principal", "credential", "revoke"}, "revoke ID", "principal credential revoke ID --etag ETAG", "etag", "yes"),
		onlineSpec([]string{"grant", "list"}, "list", "grant list", "limit", "cursor", "principal-id", "server-id"),
		onlineSpec([]string{"grant", "get"}, "get ID", "grant get ID"),
		onlineSpec([]string{"grant", "create"}, "create", "grant create --file PATH", "file"),
		onlineSpec([]string{"grant", "delete"}, "delete ID", "grant delete ID", "yes"),
		onlineSpec([]string{"grant-request", "list"}, "list", "grant-request list", "limit", "cursor", "principal-id", "state"),
		onlineSpec([]string{"grant-request", "get"}, "get REQUEST_ID", "grant-request get REQUEST_ID"),
		onlineSpec([]string{"grant-request", "approve"}, "approve REQUEST_ID", "grant-request approve REQUEST_ID --etag ETAG --file PATH", "etag", "file", "yes"),
		onlineSpec([]string{"grant-request", "reject"}, "reject REQUEST_ID", "grant-request reject REQUEST_ID --etag ETAG --file PATH", "etag", "file", "yes"),
		onlineSpec([]string{"invocation", "list"}, "list", "invocation list", "limit", "cursor", "principal-id", "server-id", "requested-name", "admission-class", "decision", "outcome"),
		onlineSpec([]string{"invocation", "get"}, "get INVOCATION_ID", "invocation get INVOCATION_ID"),
	}
}

func onlineSpec(path []string, use, manifestUse string, flags ...string) onlineCommandSpec {
	return onlineCommandSpec{Path: path, Use: use, ManifestUse: manifestUse, Flags: flags}
}
