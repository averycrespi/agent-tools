package main

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func historyExportPath(options *onlineOptions) (string, error) {
	limit := options.limit
	if limit == 0 {
		limit = contract.HistoryExportMaxRecords
	}
	if limit < 1 || limit > contract.HistoryExportMaxRecords {
		return "", fmt.Errorf("invalid export limit")
	}
	after := "0"
	if value := options.filters["after-sequence"]; value != nil && *value != "" {
		after = *value
	}
	sequence, err := strconv.ParseInt(after, 10, 64)
	if err != nil || sequence < 0 || strconv.FormatInt(sequence, 10) != after {
		return "", fmt.Errorf("invalid export sequence")
	}
	query := url.Values{"after_sequence": {after}, "limit": {strconv.Itoa(limit)}}
	return "/api/v2/history/export?" + query.Encode(), nil
}

func runHistoryExport(command *cobra.Command, options *onlineOptions) error {
	path, err := historyExportPath(options)
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("Use a nonnegative --after-sequence and --limit between 1 and 256."))
	}
	return runOnlineRead(command, options, path, historyExportTable)
}

func historyExportTable(body []byte) (controlclient.Table, error) {
	var value contract.HistoryExport
	if err := controlclient.DecodeResponse(body, &value); err != nil {
		return controlclient.Table{}, err
	}
	if len(body) > contract.HistoryExportMaxBytes || value.Format != 1 || !contract.ValidAuditID(value.InstallationID) || !contract.ValidAuditID(value.Generation) || value.CompleteTrafficAudit || value.Absence != contract.HistoryExportAbsence || value.Records == nil || len(value.Records) > contract.HistoryExportMaxRecords || value.Retained < int64(len(value.Records)) {
		return controlclient.Table{}, fmt.Errorf("invalid history export")
	}
	previous, err := strconv.ParseInt(value.AfterSequence, 10, 64)
	if err != nil || previous < 0 {
		return controlclient.Table{}, fmt.Errorf("invalid history export sequence")
	}
	for _, record := range value.Records {
		sequence, err := strconv.ParseInt(record.Sequence, 10, 64)
		if err != nil || sequence <= previous {
			return controlclient.Table{}, fmt.Errorf("invalid history export sequence")
		}
		previous = sequence
		switch record.Protocol {
		case "mcp":
			if record.MCP == nil || record.HTTP != nil || record.Git != nil {
				return controlclient.Table{}, fmt.Errorf("invalid MCP export")
			}
		case "http":
			if record.HTTP == nil || record.MCP != nil || record.Git != nil || !validHTTPTrafficItem(*record.HTTP) {
				return controlclient.Table{}, fmt.Errorf("invalid HTTP export")
			}
		case "git":
			if record.Git == nil || record.MCP != nil || record.HTTP != nil || !validGitTraffic(*record.Git) {
				return controlclient.Table{}, fmt.Errorf("invalid Git export")
			}
		default:
			return controlclient.Table{}, fmt.Errorf("invalid export protocol")
		}
	}
	if value.NextSequence != strconv.FormatInt(previous, 10) {
		return controlclient.Table{}, fmt.Errorf("invalid export coverage")
	}
	return controlclient.Table{Headers: []string{"Generation", "Records", "Retained", "Truncated", "Next sequence"}, Rows: [][]string{{value.Generation, strconv.Itoa(len(value.Records)), strconv.FormatInt(value.Retained, 10), strconv.FormatBool(value.Truncated), value.NextSequence}}, Notes: []string{"Installation: " + value.InstallationID + "; captured: " + value.CapturedAt + "; high-water: " + value.HighWater + "; pruning: " + value.Pruning, value.Absence, "This is not a complete traffic audit. Use --json to export the records."}}, nil
}
