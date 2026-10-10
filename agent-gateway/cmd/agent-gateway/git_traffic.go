package main

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func runGitTrafficRead(command *cobra.Command, options *onlineOptions, args []string, action string) error {
	if action == "get" {
		if len(args) != 1 || !gatewayIDPattern.MatchString(args[0]) {
			return writeOnlineFailure(command, options.output, controlclient.NewInputError("The Git traffic ID is invalid."))
		}
		return runOnlineRead(command, options, "/api/v2/git/traffic/"+args[0], gitTrafficItemTable)
	}
	path, err := controlclient.BuildListPath("/api/v2/git/traffic", controlclient.ListOptions{Limit: options.limit, Cursor: options.cursor})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	return runOnlineRead(command, options, path, gitTrafficListTable)
}
func validGitTrafficRef(ref contract.GitRevisionRef) bool {
	return contract.ValidAuditID(ref.ID) && validCanonicalRevision(ref.Revision) && ref.Revision != "0"
}
func validGitTraffic(item contract.GitTrafficRecord) bool {
	a := item.Admission
	if !contract.ValidGitTrafficRefs(a) {
		return false
	}
	if !validGitTrafficRef(a.Principal) || !validGitTrafficRef(a.AgentCredential) || !validCanonicalRevision(a.ProfileRevision) || a.ProfileRevision == "0" || !validCanonicalRevision(a.AuthorizationRevision) || a.AuthorizationRevision == "0" {
		return false
	}
	admitted, ok := httpResponseTime(a.AdmittedAt)
	evaluated, valid := httpResponseTime(a.EvaluatedAt)
	if !ok || !valid || evaluated.Before(admitted) {
		return false
	}
	if !slices.Contains([]string{"", "unsupported", "repository_unavailable", "destination_unavailable"}, a.Rejection) || !slices.Contains([]string{"", "credential_unavailable"}, a.Denial) || (a.Rejection != "" || a.Denial != "") && a.Allowed {
		return false
	}
	if (a.Operation == "invalid") != (a.Rejection != "") || (a.Operation == "push") != (a.Commands > 0) {
		return false
	}
	if a.Rejection != "" && (a.Repository != (contract.GitRevisionRef{}) || a.AliasRevision != "" || a.Policy != nil || a.Material != nil) {
		return false
	}
	if a.Rejection == "" && (!validGitTrafficRef(a.Repository) || !validCanonicalRevision(a.AliasRevision) || a.AliasRevision == "0") {
		return false
	}
	if p := a.Policy; p != nil {
		if !validGrantDescription(p.RepositoryName) || p.RepositoryName == "" || len(p.RepositoryName) > 256 || !validGitLocatorResponse(p.RepositoryURL) || p.Grants == nil || len(p.Grants) > 4 || p.GrantCount < len(p.Grants) || p.GrantCount > contract.GitGrants || p.Creates < 0 || p.Updates < 0 || p.Deletes < 0 || p.Creates+p.Updates+p.Deletes != a.Commands {
			return false
		}
		seen := map[string]bool{}
		for _, g := range p.Grants {
			if !validGitTrafficRef(g) || seen[g.ID] {
				return false
			}
			seen[g.ID] = true
		}
	}
	if m := a.Material; m != nil && (!a.Allowed || !validGitTrafficRef(m.Credential) || !validCanonicalRevision(m.Generation) || m.Generation == "0") {
		return false
	}
	if p := a.PrivateGrant; p != nil && (!contract.ValidAuditID(p.ID) || p.Revision < 1) {
		return false
	}
	if !contract.ValidAuditID(a.ID) || a.Rejection == "" && (!validCanonicalRevision(a.Repository.Revision) || !contract.ValidAuditID(a.Repository.ID)) || a.Commands < 0 || a.Commands > contract.GitRequestedRefs {
		return false
	}
	if _, ok := httpResponseTime(a.AdmittedAt); !ok {
		return false
	}
	if !slices.Contains([]string{"read", "read_discovery", "push", "push_discovery", "probe", "invalid"}, a.Operation) {
		return false
	}
	if c := item.Completion; c != nil {
		if !contract.ValidGitTrafficOutcomes(a, *c) {
			return false
		}
		completed, valid := httpResponseTime(c.CompletedAt)
		if !valid || completed.Before(evaluated) || c.Status != 0 && (c.Status < 100 || c.Status > 599) || c.TransferComplete && c.Status == 0 || !slices.Contains([]string{"", "credential_unavailable", "authorization_unavailable"}, c.Failure) || c.Failure != "" && c.Outcome != "prestart_failure" {
			return false
		}
		if c.Outcome == "prestart_failure" && (c.Status != 0 || c.BytesSent != 0 || c.BytesReceived != 0 || c.TransferComplete) || c.Outcome == "nonmutation" && (!c.TransferComplete || a.Operation == "push") {
			return false
		}
		if !a.Allowed || !slices.Contains([]string{"nonmutation", "outcome_unknown", "prestart_failure"}, c.Outcome) || !slices.Contains([]string{"", "reported_success", "reported_failure", "reported_partial"}, c.ReportedResult) || c.BytesSent < 0 || c.BytesReceived < 0 || c.DurationMS < 0 {
			return false
		}
		if c.ReportedResult != "" && (a.Operation != "push" || !c.TransferComplete || c.Status != 200) {
			return false
		}
	}
	return true
}
func gitTrafficLabel(value string) string {
	switch value {
	case "read":
		return "Read"
	case "read_discovery":
		return "Read discovery"
	case "push":
		return "Push"
	case "push_discovery":
		return "Push discovery"
	case "probe":
		return "Push probe"
	case "invalid":
		return "Unsupported exchange"
	case "unsupported":
		return "Unsupported Git request"
	case "repository_unavailable":
		return "Repository unavailable"
	case "destination_unavailable":
		return "Destination unavailable"
	case "credential_unavailable":
		return "Credential unavailable"
	case "authorization_unavailable":
		return "Authorization unavailable"
	case "reported_success":
		return "Reported success"
	case "reported_failure":
		return "Reported failure"
	case "reported_partial":
		return "Reported partial success"
	default:
		return ""
	}
}
func gitTrafficFacts(item contract.GitTrafficRecord) (decision, transport, result string) {
	decision, transport, result = "Blocked", "Not dispatched", "Not applicable"
	if !item.Admission.Allowed {
		if item.Admission.Rejection != "" {
			decision = gitTrafficLabel(item.Admission.Rejection)
		}
		if item.Admission.Denial != "" {
			decision = gitTrafficLabel(item.Admission.Denial)
		}
		return
	}
	decision, transport, result = "Allowed", "Unknown", "Unknown"
	if c := item.Completion; c != nil {
		switch {
		case c.Outcome == "prestart_failure":
			transport = "Not started"
			if c.Failure != "" {
				transport = gitTrafficLabel(c.Failure)
			}
		case c.TransferComplete:
			transport = "Complete"
		default:
			transport = "Incomplete"
		}
		if c.ReportedResult != "" {
			result = gitTrafficLabel(c.ReportedResult)
		}
	}
	if item.Admission.Operation != "push" {
		result = "Not a push"
	}
	return
}
func gitTrafficListTable(body []byte) (controlclient.Table, error) {
	var page contract.GitTrafficPage
	if err := controlclient.DecodeExactResponse(body, &page); err != nil {
		return controlclient.Table{}, err
	}
	if page.Items == nil || len(page.Items) > 100 || page.NextCursor != nil && (len(*page.NextCursor) == 0 || len(*page.NextCursor) > 512 || len(page.Items) == 0) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	table := controlclient.Table{Headers: []string{"ADMITTED", "REPOSITORY", "EXCHANGE", "OUTCOME", "ADMISSION", "TRANSPORT", "UPSTREAM REPORT", "ID"}, NextCursor: page.NextCursor}
	seen := map[string]bool{}
	for _, item := range page.Items {
		if !validGitTraffic(item) || seen[item.Admission.ID] {
			return controlclient.Table{}, controlclient.ErrResponseInvalid
		}
		seen[item.Admission.ID] = true
		d, t, r := gitTrafficFacts(item)
		a := item.Admission
		table.Rows = append(table.Rows, []string{a.AdmittedAt, a.Repository.ID, gitTrafficLabel(a.Operation), contract.GitTrafficOutcome(item), d, t, r, a.ID})
	}
	return table, nil
}
func gitTrafficItemTable(body []byte) (controlclient.Table, error) {
	var item contract.GitTrafficRecord
	if err := controlclient.DecodeExactResponse(body, &item); err != nil {
		return controlclient.Table{}, err
	}
	if !validGitTraffic(item) {
		return controlclient.Table{}, controlclient.ErrResponseInvalid
	}
	a := item.Admission
	d, t, r := gitTrafficFacts(item)
	table := controlclient.Table{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{{"ID", a.ID}, {"Repository", a.Repository.ID + " revision " + a.Repository.Revision}, {"Agent", a.Principal.ID}, {"Exchange", gitTrafficLabel(a.Operation)}, {"Commands", strconv.Itoa(a.Commands)}, {"Outcome", contract.GitTrafficOutcome(item)}, {"Admission", d}, {"Transport", t}, {"Upstream report", r}, {"Evidence", "Admission references describe historical policy, not current authority. Reports are not independently verified effects. Reconcile uncertain pushes with the remote before deciding on another operation."}}}
	if a.Operation == "push" {
		state := "Unavailable (legacy record)"
		if e := a.RefEvidence; e != nil {
			state = fmt.Sprintf("%s: %d of %d requested refs", e.State, len(e.Refs), a.Commands)
			for i, ref := range e.Refs {
				outcome := "Unknown"
				if !a.Allowed {
					outcome = "Not dispatched"
				} else if c := item.Completion; c != nil && len(c.RefOutcomes) > i {
					if c.RefOutcomes[i] == "ok" {
						outcome = "Reported success"
					} else {
						outcome = "Reported failure"
					}
				}
				table.Rows = append(table.Rows, []string{"Requested ref", fmt.Sprintf("%s | %s | %s", ref.Name, ref.Action, outcome)})
			}
		}
		table.Rows = append(table.Rows, []string{"Ref evidence", state})
	}
	if p := a.Policy; p != nil {
		table.Rows = append(table.Rows, []string{"Repository at admission", p.RepositoryName}, []string{"Canonical destination at admission", p.RepositoryURL}, []string{"Create/update/delete commands", fmt.Sprintf("%d / %d / %d", p.Creates, p.Updates, p.Deletes)}, []string{"Applicable grant references retained", fmt.Sprintf("%d of %d", len(p.Grants), p.GrantCount)})
		for _, g := range p.Grants {
			table.Rows = append(table.Rows, []string{"Admission-time grant", g.ID + " revision " + g.Revision})
		}
	}
	if c := item.Completion; c != nil {
		table.Rows = append(table.Rows, []string{"HTTP status", strconv.Itoa(c.Status)}, []string{"Bytes sent/received", fmt.Sprintf("%d / %d", c.BytesSent, c.BytesReceived)})
	}
	return table, nil
}
