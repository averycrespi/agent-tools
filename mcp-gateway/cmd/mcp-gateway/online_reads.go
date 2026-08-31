package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var gatewayIDPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

func runOnlineCommand(command *cobra.Command, spec onlineCommandSpec, options *onlineOptions, args []string) error {
	switch strings.Join(spec.Path, " ") {
	case "status":
		return runOnlineRead(command, options, "/api/v1/system-status", statusTable)
	case "admin credential list":
		path, err := controlclient.BuildListPath("/api/v1/admin-credentials", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, adminCredentialListTable)
	case "admin credential get":
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The admin credential ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v1/admin-credentials/"+args[0], adminCredentialItemTable)
	case "admin credential create":
		return runAdminCredentialCreate(command, options)
	case "admin credential rotate":
		return runAdminCredentialRotate(command, options, args)
	case "admin credential revoke":
		return runAdminCredentialRevoke(command, options, args)
	case "backup list":
		path, err := controlclient.BuildListPath("/api/v1/backups", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, backupListTable)
	case "backup get":
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The backup ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v1/backups/"+args[0], backupItemTable)
	case "backup create":
		return runBackupCreate(command, options)
	case "backup delete":
		return runBackupDelete(command, options, args)
	case "invocation list":
		path, err := invocationListPath(options)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, invocationListTable)
	case "server create":
		return runServerCreate(command, options)
	case "server update":
		return runServerUpdate(command, options, args)
	case "server delete":
		return runServerDelete(command, options, args)
	case "server list":
		path, err := controlclient.BuildListPath("/api/v1/servers", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, serverListTable)
	case "server get":
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server ID is invalid."))
		}
		return runOnlineItemRead(command, options, onlineItemServer, args[0], serverItemTable)
	case "server auth-flow list":
		path, err := authFlowListPath(options, args)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, authFlowListTable)
	case "server auth-flow get":
		if len(args) != 2 || !gatewayIDPattern.MatchString(args[0]) || !gatewayIDPattern.MatchString(args[1]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server or auth-flow ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v1/servers/"+args[0]+"/auth-flows/"+args[1], authFlowItemTable)
	case "server auth-flow start":
		return runServerAuthFlowStart(command, options, args)
	case "server auth-flow cancel":
		return runServerAuthFlowCancel(command, options, args)
	case "server credential replace":
		return runServerCredentialReplace(command, options, args)
	case "server operation list":
		path, err := operationListPath(options, args)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, operationListTable)
	case "server operation get":
		if len(args) != 2 || !gatewayIDPattern.MatchString(args[0]) || !gatewayIDPattern.MatchString(args[1]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server or operation ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v1/servers/"+args[0]+"/operations/"+args[1], operationItemTable)
	case "server operation start":
		return runServerOperationStart(command, options, args)
	case "server descriptor list":
		path, err := descriptorListPath(options, args)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, descriptorListTable)
	case "server descriptor get":
		if len(args) != 2 || !gatewayIDPattern.MatchString(args[0]) || !gatewayIDPattern.MatchString(args[1]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The server or tool ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v1/servers/"+args[0]+"/descriptors/"+args[1], descriptorItemTable)
	case "catalog list":
		path, err := controlclient.BuildListPath("/api/v1/catalog", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, catalogTable)
	case "principal list":
		path, err := controlclient.BuildListPath("/api/v1/principals", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, principalListTable)
	case "principal get":
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The principal ID is invalid."))
		}
		return runOnlineItemRead(command, options, onlineItemPrincipal, args[0], principalItemTable)
	case "principal create":
		return runPrincipalCreate(command, options)
	case "principal update":
		return runPrincipalUpdate(command, options, args)
	case "principal credential issue":
		return runPrincipalCredentialIssue(command, options, args)
	case "principal credential rotate":
		return runPrincipalCredentialRotate(command, options, args)
	case "principal credential revoke":
		return runPrincipalCredentialRevoke(command, options, args)
	case "grant list":
		path, err := grantListPath(options)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, grantListTable)
	case "grant get":
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v1/grants/"+args[0], grantItemTable)
	case "grant create":
		return runGrantCreate(command, options)
	case "grant delete":
		return runGrantDelete(command, options, args)
	case "grant-request list":
		path, err := grantRequestListPath(options)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, grantRequestListTable)
	case "grant-request get":
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The grant-request ID is invalid."))
		}
		return runOnlineItemRead(command, options, onlineItemGrantRequest, args[0], grantRequestItemTable)
	case "grant-request approve":
		return runGrantRequestApprove(command, options, args)
	case "grant-request reject":
		return runGrantRequestReject(command, options, args)
	case "invocation get":
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The invocation ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v1/invocations/"+args[0], invocationItemTable)
	default:
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("This online command is not implemented yet."))
	}
}

func runOnlineRead(command *cobra.Command, options *onlineOptions, path string, table func([]byte) (controlclient.Table, error)) error {
	mode, err := controlclient.ParseOutputMode(options.output)
	if err != nil {
		return writeOnlineFailure(command, string(controlclient.OutputTable), controlclient.NewInputError("The output mode is invalid."))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodGet, Path: path, Header: header})
	if err != nil {
		return writeOnlineFailure(command, options.output, classifyReadFailure(err))
	}
	if failure := evaluateOnlineResponse(response, options.adminBearer.path); failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	projection, err := table(response.Body)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	if mode == controlclient.OutputJSON {
		if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, response.Body, controlclient.Table{}); err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return nil
	}
	if err := controlclient.WriteSuccess(command.OutOrStdout(), mode, nil, projection); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The command output could not be written."))
	}
	return nil
}

func classifyReadFailure(err error) *controlclient.OnlineError {
	return controlclient.ClassifyRequestError(err, controlclient.RequestPhaseRead)
}

func withNextCursor(table controlclient.Table, nextCursor *string) controlclient.Table {
	table.NextCursor = nextCursor
	return table
}

func invocationListPath(options *onlineOptions) (string, error) {
	filters := make(map[string]string)
	for cliName, apiName := range map[string]string{
		"principal-id": "principal_id", "server-id": "server_id", "requested-name": "requested_name",
		"admission-class": "admission_class", "decision": "decision", "outcome": "outcome",
	} {
		if value := options.filters[cliName]; value != nil && *value != "" {
			filters[apiName] = *value
		}
	}
	return controlclient.BuildListPath("/api/v1/invocations", controlclient.ListOptions{
		Limit: options.limit, Cursor: options.cursor, Filters: filters,
		AllowedFilters: []string{"principal_id", "server_id", "requested_name", "admission_class", "decision", "outcome"},
	})
}

type serverWire struct {
	ID                  string                         `json:"id"`
	Namespace           string                         `json:"namespace"`
	DisplayName         string                         `json:"display_name"`
	DesiredState        contract.DesiredServerState    `json:"desired_state"`
	DesiredRevision     string                         `json:"desired_revision"`
	Transport           json.RawMessage                `json:"transport"`
	CredentialRevisions contract.CredentialRevisions   `json:"credential_revisions"`
	CredentialState     contract.ServerCredentialState `json:"credential_state"`
	Runtime             contract.ServerRuntime         `json:"runtime"`
	Catalog             contract.ServerCatalog         `json:"catalog"`
	CreatedAt           string                         `json:"created_at"`
	UpdatedAt           string                         `json:"updated_at"`
	DeletedAt           *string                        `json:"deleted_at"`
}

func descriptorListPath(options *onlineOptions, args []string) (string, error) {
	if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
		return "", controlclient.ErrInvalidInput
	}
	filters := map[string]string{}
	if value := options.filters["retired"]; value != nil && *value != "" {
		if *value != "include" && *value != "exclude" && *value != "only" {
			return "", controlclient.ErrInvalidInput
		}
		filters["retired"] = *value
	}
	return controlclient.BuildListPath("/api/v1/servers/"+args[0]+"/descriptors", controlclient.ListOptions{
		Limit: options.limit, Cursor: options.cursor, Filters: filters, AllowedFilters: []string{"retired"},
	})
}

func serverListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[serverWire]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	rows := make([][]string, 0, len(page.Items))
	for _, server := range page.Items {
		rows = append(rows, serverRow(server))
	}
	return controlclient.Table{Headers: serverHeaders(), Rows: rows, NextCursor: page.NextCursor}, nil
}

func serverItemTable(body []byte) (controlclient.Table, error) {
	var server serverWire
	if err := controlclient.DecodeResponse(body, &server); err != nil {
		return controlclient.Table{}, err
	}
	return controlclient.Table{Headers: serverHeaders(), Rows: [][]string{serverRow(server)}}, nil
}

func serverHeaders() []string {
	return []string{"ID", "DISPLAY", "NAMESPACE", "DESIRED", "RUNTIME", "CREDENTIAL", "DURABLE", "ACTIVE", "UPDATED"}
}

func serverRow(server serverWire) []string {
	return []string{
		server.ID, server.DisplayName, server.Namespace, string(server.DesiredState), string(server.Runtime.State), string(server.CredentialState),
		fmt.Sprintf("%s revision=%s tools=%d", server.Catalog.DurableState, pointerText(server.Catalog.DurableRevision), server.Catalog.DurableToolCount),
		fmt.Sprintf("%s revision=%s tools=%d", server.Catalog.ActiveState, pointerText(server.Catalog.ActiveRevision), server.Catalog.ActiveToolCount), server.UpdatedAt,
	}
}

func descriptorListTable(body []byte) (controlclient.Table, error) {
	var page contract.Collection[contract.ToolDescriptor]
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	return withNextCursor(descriptorTable(page.Items), page.NextCursor), nil
}

func descriptorItemTable(body []byte) (controlclient.Table, error) {
	var item contract.ToolDescriptor
	if err := controlclient.DecodeResponse(body, &item); err != nil {
		return controlclient.Table{}, err
	}
	return descriptorTable([]contract.ToolDescriptor{item}), nil
}

func descriptorTable(items []contract.ToolDescriptor) controlclient.Table {
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		state := "current"
		if item.RetiredAt != nil {
			state = "retired"
		}
		rows = append(rows, []string{item.ID, item.ExternalName, item.UpstreamName, item.ServerID, state, item.CatalogRevision, item.Fingerprint, item.LastSeenAt})
	}
	return controlclient.Table{Headers: []string{"ID", "EXTERNAL", "UPSTREAM", "SERVER", "EVIDENCE", "REVISION", "FINGERPRINT", "LAST_SEEN"}, Rows: rows}
}

func catalogTable(body []byte) (controlclient.Table, error) {
	var page contract.CatalogPage
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	rows := make([][]string, 0, len(page.Items)+1)
	rows = append(rows, []string{"CATALOG", "-", "-", "-", string(page.Catalog.ActiveState), page.Catalog.ActiveGeneration, fmt.Sprintf("issues=%d", page.Catalog.IssueCount), pointerText(page.Catalog.ChangedAt)})
	for _, item := range page.Items {
		rows = append(rows, []string{item.ID, item.ExternalName, item.UpstreamName, item.ServerID, "published evidence", item.CatalogRevision, item.Fingerprint, item.LastSeenAt})
	}
	return controlclient.Table{Headers: []string{"ID", "EXTERNAL", "UPSTREAM", "SERVER", "CATALOG", "REVISION", "FINGERPRINT", "CHANGED/LAST_SEEN"}, Rows: rows, NextCursor: page.NextCursor}, nil
}

func pointerText(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}

func statusTable(body []byte) (controlclient.Table, error) {
	var status contract.SystemStatus
	if err := controlclient.DecodeResponse(body, &status); err != nil {
		return controlclient.Table{}, err
	}
	lastBackup := "never"
	if status.Backup.LastCompletedAt != nil {
		lastBackup = *status.Backup.LastCompletedAt
	}
	rows := [][]string{
		{"process", string(status.Process.State), fmt.Sprintf("ready=%t started_at=%s", status.Process.Ready, status.Process.StartedAt)},
		{"sqlite", string(status.SQLite.State), fmt.Sprintf("schema=%s revision=%s latched=%t", status.SQLite.SchemaVersion, status.SQLite.Revision, status.SQLite.Latched)},
		{"keyring", string(status.Keyring.Capability), "OS-managed capability; later operations may still interact or fail"},
		{"backup", string(status.Backup.State), "last_completed_at=" + lastBackup},
		{"protocols", string(status.Protocols.AgentAuth), fmt.Sprintf("modern=%s legacy=%s", status.Protocols.Modern, status.Protocols.Legacy)},
	}
	for _, limit := range statusLimits(status.Limits) {
		state := "available"
		if limit.value.Saturated {
			state = "saturated"
		}
		rows = append(rows, []string{"limit." + limit.name, state, fmt.Sprintf("in_use=%d limit=%d", limit.value.InUse, limit.value.Limit)})
	}
	return controlclient.Table{Headers: []string{"AREA", "STATE", "DETAIL"}, Rows: rows}, nil
}

type namedLimit struct {
	name  string
	value contract.LimitStatus
}

func statusLimits(limits contract.LimitsStatus) []namedLimit {
	return []namedLimit{
		{"http_regular", limits.HTTPRegular}, {"http_control_auth", limits.HTTPControlAuth}, {"http_admin", limits.HTTPAdmin}, {"http_health", limits.HTTPHealth},
		{"mcp_work", limits.MCPWork}, {"mcp_streams", limits.MCPStreams}, {"admin_sessions", limits.AdminSessions}, {"legacy_sessions", limits.LegacySessions},
		{"event_streams", limits.EventStreams}, {"backup_work", limits.BackupWork}, {"backup_records", limits.BackupRecords}, {"admin_credentials", limits.AdminCredentials},
		{"idempotency_records", limits.IdempotencyRecords}, {"keyring_candidates", limits.KeyringCandidates}, {"keyring_work", limits.KeyringWork}, {"database_bytes", limits.DatabaseBytes},
		{"server_identities", limits.ServerIdentities}, {"servers", limits.Servers}, {"downstream_runtimes", limits.DownstreamRuntimes}, {"server_reconciliations", limits.ServerReconciliations},
		{"catalog_traversals", limits.CatalogTraversals}, {"oauth_flows", limits.OAuthFlows}, {"oauth_callback_work", limits.OAuthCallbackWork}, {"s2_idempotency_records", limits.S2IdempotencyRecords},
		{"active_tools", limits.ActiveTools}, {"durable_tool_identities", limits.DurableToolIdentities}, {"downstream_dispatch", limits.DownstreamDispatch}, {"principals", limits.Principals},
		{"grants", limits.Grants}, {"grant_requests", limits.GrantRequests}, {"grant_request_evidence_bytes", limits.GrantRequestEvidenceBytes},
	}
}

func invocationListTable(body []byte) (controlclient.Table, error) {
	var page contract.InvocationPage
	if err := controlclient.DecodeResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	rows := make([][]string, 0, len(page.Items)+1)
	unknown := false
	for _, item := range page.Items {
		rows = append(rows, invocationSummaryRow(item))
		unknown = unknown || item.Outcome.Class == contract.InvocationOutcomeUnknown
	}
	if unknown {
		rows = append(rows, invocationGuidanceRow())
	}
	return controlclient.Table{Headers: invocationHeaders(), Rows: rows, NextCursor: page.NextCursor}, nil
}

func invocationItemTable(body []byte) (controlclient.Table, error) {
	var item contract.Invocation
	if err := controlclient.DecodeResponse(body, &item); err != nil {
		return controlclient.Table{}, err
	}
	rows := [][]string{invocationSummaryRow(item.InvocationSummary)}
	if item.Outcome.Class == contract.InvocationOutcomeUnknown {
		rows = append(rows, invocationGuidanceRow())
	}
	return controlclient.Table{Headers: invocationHeaders(), Rows: rows}, nil
}

func invocationHeaders() []string {
	return []string{"ID", "ADMITTED", "PRINCIPAL", "REQUESTED", "TARGET", "DECISION", "OUTCOME", "BASIS"}
}

func invocationSummaryRow(item contract.InvocationSummary) []string {
	requested := "-"
	if item.RequestedName != nil {
		requested = *item.RequestedName
	}
	decision := "-"
	if item.Authorization != nil {
		decision = string(item.Authorization.Decision)
	}
	return []string{item.ID, item.AdmittedAt, item.PrincipalID, requested, invocationTargetLabel(item.Target), decision, string(item.Outcome.Class), string(item.Outcome.Basis)}
}

func invocationTargetLabel(target *contract.InvocationTarget) string {
	if target == nil {
		return "-"
	}
	if target.Kind == contract.InvocationTargetGateway {
		return "gateway:" + target.UpstreamName
	}
	return target.ServerID + ":" + target.UpstreamName
}

func invocationGuidanceRow() []string {
	return []string{
		"GUIDANCE", "-", "-", "-", "-", "-",
		"Missing terminal evidence does not prove nonexecution. Gateway does not automatically replay; an explicit retry may duplicate an effect.",
		string(contract.InvocationBasisMissingTerminal),
	}
}
