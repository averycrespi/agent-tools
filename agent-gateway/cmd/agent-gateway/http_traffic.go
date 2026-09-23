package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func runHTTPTrafficRead(command *cobra.Command, options *onlineOptions, args []string, action string) error {
	if action == "get" {
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The HTTP traffic ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v2/http/traffic/"+args[0], httpTrafficItemTable)
	}
	filters := map[string]string{}
	for _, key := range []string{"principal-id", "destination", "type", "decision", "outcome"} {
		if value := options.filters[key]; value != nil && *value != "" {
			apiKey := key
			if key == "principal-id" {
				apiKey = "principal_id"
			}
			filters[apiKey] = *value
		}
	}
	path, err := controlclient.BuildListPath("/api/v2/http/traffic", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor, Filters: filters, AllowedFilters: []string{"principal_id", "destination", "type", "decision", "outcome"}})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	return runOnlineRead(command, options, path, httpTrafficListTable)
}

func httpTrafficDestination(t *contract.HTTPTrafficTarget) string {
	if t == nil {
		return "Not parsed"
	}
	destination := net.JoinHostPort(t.Host, strconv.Itoa(int(t.Port)))
	if t.Scheme != "" {
		return t.Method + " " + t.Scheme + "://" + destination
	}
	return "CONNECT " + destination
}

func httpTrafficListTable(body []byte) (controlclient.Table, error) {
	var page contract.HTTPTrafficPage
	if err := controlclient.DecodeExactResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	if page.Items == nil || len(page.Items) > 100 {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	if page.NextCursor != nil {
		raw, err := base64.RawURLEncoding.DecodeString(*page.NextCursor)
		if err != nil || len(*page.NextCursor) == 0 || len(*page.NextCursor) > 512 || base64.RawURLEncoding.EncodeToString(raw) != *page.NextCursor || len(page.Items) == 0 {
			return controlclient.Table{}, controlclient.ErrResponseInvalid
		}
	}
	table := controlclient.Table{Headers: []string{"ADMITTED", "DESTINATION", "AGENT", "TYPE", "DECISION", "OUTCOME", "ID"}, NextCursor: page.NextCursor}
	seen := map[string]bool{}
	for _, item := range page.Items {
		if !validHTTPTrafficSummary(item) || seen[item.ID] {
			return controlclient.Table{}, controlclient.ErrResponseInvalid
		}
		seen[item.ID] = true
		table.Rows = append(table.Rows, []string{item.AdmittedAt, httpTrafficDestination(item.Target), item.PrincipalID, item.Type, item.Decision, item.Outcome, item.ID})
	}
	return table, nil
}

func httpTrafficItemTable(body []byte) (controlclient.Table, error) {
	var item contract.HTTPTrafficRecord
	if err := controlclient.DecodeExactResponse(body, &item); err != nil {
		return controlclient.Table{}, err
	}
	if !validHTTPTrafficItem(item) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	a := item.Admission
	table := controlclient.Table{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{{"ID", a.ID}, {"Admitted", a.AdmittedAt}, {"Destination", httpTrafficDestination(a.Target)}, {"Agent", fmt.Sprintf("%s revision %d", a.Principal.ID, a.Principal.Revision)}, {"Agent credential", fmt.Sprintf("%s revision %d", a.AgentCredential.ID, a.AgentCredential.Revision)}, {"Evaluated", a.EvaluatedAt}, {"Evidence", "Policy and material references describe admission time, not current authority."}}}
	if a.Decision != nil {
		d := a.Decision
		table.Rows = append(table.Rows, []string{"Decision", fmt.Sprintf("allowed=%t; %s; policy revision %d; default revision %d", d.Allowed, d.Reason, d.PolicyRevision, d.DefaultRevision)}, []string{"Transport", string(d.Transport)})
		if d.Transport == contract.HTTPTransportTunnel {
			table.Rows = append(table.Rows, []string{"Visibility", "Opaque tunnel; no inner-request visibility."})
		}
	}
	for _, grant := range a.Grants {
		policy, err := json.Marshal(grant.Policy)
		if err != nil {
			return controlclient.Table{}, err
		}
		table.Rows = append(table.Rows, []string{"Admission-time grant", fmt.Sprintf("%s revision %d: %s", grant.Reference.ID, grant.Reference.Revision, policy)})
	}
	if a.Material != nil {
		table.Rows = append(table.Rows, []string{"Admission-time injection credential", fmt.Sprintf("%s revision %d generation %s", a.Material.Credential.ID, a.Material.Credential.Revision, a.Material.Generation)})
	}
	if c := item.Completion; c != nil {
		table.Rows = append(table.Rows, []string{"Outcome", c.Outcome}, []string{"Completed", c.CompletedAt}, []string{"HTTP status", strconv.Itoa(c.Status)}, []string{"Bytes sent/received", fmt.Sprintf("%d / %d", c.BytesSent, c.BytesReceived)}, []string{"Duration (ms)", strconv.FormatInt(c.DurationMS, 10)})
	} else if a.Decision != nil && a.Decision.Allowed {
		table.Rows = append(table.Rows, []string{"Outcome", "Unknown: missing terminal evidence does not prove nonexecution or safe retry."})
	} else {
		table.Rows = append(table.Rows, []string{"Outcome", "Not dispatched"})
	}
	return table, nil
}
