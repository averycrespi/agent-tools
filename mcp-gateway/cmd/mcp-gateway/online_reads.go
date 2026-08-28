package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

var invocationIDPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

func runOnlineCommand(command *cobra.Command, spec onlineCommandSpec, options *onlineOptions, args []string) error {
	switch strings.Join(spec.Path, " ") {
	case "status":
		return runOnlineRead(command, options, "/api/v1/system-status", statusTable)
	case "invocation list":
		path, err := invocationListPath(options)
		if err != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
		}
		return runOnlineRead(command, options, path, invocationListTable)
	case "invocation get":
		if len(args) != 1 || !invocationIDPattern.MatchString(args[0]) {
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
	bearer, err := controlclient.AcquireAdminBearer(controlclient.BearerOptions{
		FilePath: options.bearerFile, ReadStdin: options.bearerStdin, Stdin: command.InOrStdin(), InputFilePath: options.file,
	})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: bearer})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodGet, Path: path, Header: header})
	if err != nil {
		return writeOnlineFailure(command, options.output, classifyReadFailure(err))
	}
	if failure := controlclient.EvaluateResponse(response); failure != nil {
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
	failure := controlclient.ClassifyClientError(err)
	if failure.Code == "client_transport_failure" || failure.Code == "client_outcome_uncertain" {
		failure.Title = "The read did not complete. This read is safe to repeat after checking Gateway availability."
	}
	return failure
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
	return controlclient.Table{Headers: invocationHeaders(), Rows: rows}, nil
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
