package main

import (
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

func doctorSummary(checks []doctorCheck) string {
	summary := "healthy"
	for _, c := range checks {
		switch c.State {
		case "failed", "not-ready", "degraded":
			return "attention"
		case "absent", "incomplete", "unavailable":
			summary = "incomplete"
		}
		if c.Name == "authenticated live status" && c.State != "ok" || c.State == "not-checked" && (c.Name == "credential setup" || c.Name == "upstream status") {
			summary = "incomplete"
		}
	}
	return summary
}

func appendDoctorSetup(result *doctorResult, facts []contract.CredentialSetupFact) {
	for _, fact := range facts {
		if !contract.ValidAuditID(fact.ID) {
			continue
		}
		name := "setup " + fact.Kind + " " + fact.ID
		// Stopped metadata and live metadata can report the same task.
		duplicate := false
		for _, check := range result.Checks {
			if check.Name == name {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		state := "incomplete"
		if fact.Inactive {
			state = "inactive"
		}
		action := ""
		switch fact.Kind {
		case "http_credential":
			action = "After serve, supply a value with agent-gateway http credential rotate " + fact.ID + " --file PATH --yes"
		case "git_credential":
			action = "After serve, supply a value with agent-gateway git credential rotate " + fact.ID + " --file PATH --yes"
		case "oauth_tokens":
			action = "After serve, explicitly authorize with agent-gateway mcp server auth-flow start " + fact.ID
		case "oauth_client", "static_credential":
			action = "After serve, supply missing material with agent-gateway mcp server credential replace " + fact.ID + " --file PATH --yes"
		default:
			continue
		}
		if fact.Inactive {
			action = ""
		}
		detail := "Credential material has never been configured."
		if fact.Action == "sign_in" {
			detail = "Fresh OAuth authorization is required; doctor did not initiate it."
		}
		if len(fact.Slots) > 0 {
			detail += " Declared slots: " + diagnostics.Text(strings.Join(fact.Slots, ", "), 256)
		}
		result.Checks = append(result.Checks, doctorCheck{Name: name, State: state, Detail: detail, Next: action})
	}
}

func appendDoctorSystem(result *doctorResult, s contract.SystemStatus) {
	add := func(name, state, detail, next string) {
		result.Checks = append(result.Checks, doctorCheck{Name: name, State: state, Detail: detail, Next: next})
	}
	if s.Process.Ready && s.Process.State == contract.ProcessReady {
		add("serving", "ok", "Selected Gateway reports ready.", "")
	} else {
		add("serving", "failed", "Selected Gateway process state: "+diagnostics.Text(string(s.Process.State), 80), "Inspect the selected Gateway logs and status; do not replay work.")
	}
	if s.SQLite.State != contract.SQLiteReady || s.SQLite.Latched {
		add("control storage", "failed", fmt.Sprintf("state=%s latched=%t", diagnostics.Text(string(s.SQLite.State), 80), s.SQLite.Latched), "Preserve storage files and inspect the selected Gateway logs before a controlled restart.")
	} else {
		add("control storage", "ok", "Serving owner reports control storage ready.", "")
	}
	if s.Keyring.Capability != contract.KeyringReady {
		add("credential custody", "failed", "Serving capability: "+diagnostics.Text(string(s.Keyring.Capability), 80), "Inspect the selected Gateway logs and custody files; do not replace established keys.")
	} else {
		add("credential custody", "ok", "Serving owner reports encrypted custody ready; no secret was resolved by doctor.", "")
	}
	if s.HTTPProxy != nil {
		state, detail, action := "inactive", "HTTP proxy deliberately disabled.", ""
		if s.HTTPProxy.Enabled {
			state, detail = "ok", "HTTP proxy ready; client CA trust remains unverified."
			if !s.HTTPProxy.Ready || !s.HTTPProxy.CAReady {
				state = "failed"
				detail = "Enabled HTTP proxy or signing material is not ready."
				action = "Inspect the selected Gateway listener and CA diagnostics; client trust is a separate operator action."
			}
		}
		add("HTTP proxy", state, detail, action)
	}
	if s.Endpoints != nil {
		state := "ok"
		action := ""
		if s.Endpoints.MCP == contract.EndpointDisabled {
			state = "inactive"
		} else if s.Endpoints.MCP != contract.EndpointReady {
			state = "failed"
			action = "Inspect the selected Gateway MCP readiness and logs."
		}
		add("MCP endpoint", state, "MCP admission: "+diagnostics.Text(string(s.Endpoints.MCP), 80), action)
	}
	if t := s.Traffic; t != nil {
		state, action := "ok", ""
		if t.State == "disabled" && t.Incident == nil && !t.Faulted && !t.Pressure {
			state = "inactive"
		} else if !t.Ready || t.Pressure || t.Faulted || t.Health == "degraded" || t.Health == "failed" {
			state = "degraded"
			action = historyAction(t)
		}
		detail := "Optional history: " + diagnostics.Text(t.State, 80) + "; serving health is independent."
		if t.Incident != nil {
			detail += " Incident: " + diagnostics.Text(t.Incident.Cause, 80) + " stage=" + diagnostics.Text(t.Incident.Stage, 80) + " settlement=" + diagnostics.Text(t.Incident.Settlement, 80)
		}
		add("traffic history", state, detail, action)
	}
	if d := s.Diagnostics; d != nil && d.State != "ready" && d.State != "writing" {
		add("diagnostic delivery", "degraded", "Diagnostic sink: "+diagnostics.Text(d.State, 80), "Inspect the configured stderr destination; no request replay.")
	}
}

func appendDoctorServers(result *doctorResult, servers []serverWire) {
	for _, server := range servers {
		if !contract.ValidAuditID(server.ID) {
			continue
		}
		name := "MCP server " + server.ID
		state, detail, action := "ok", "Server runtime: "+diagnostics.Text(string(server.Runtime.State), 80), ""
		switch {
		case server.DesiredState == contract.DesiredServerDisabled || server.DesiredState == contract.DesiredServerDeleted:
			state = "inactive"
			detail = "Server deliberately disabled or deleted; no activation attempted."
		case server.CredentialState == contract.ServerCredentialReauthenticationRequired || server.Runtime.State == contract.RuntimeAuthenticationRequired:
			state = "incomplete"
			action = "Explicitly authorize with agent-gateway mcp server auth-flow start " + server.ID
		case server.CredentialState == contract.ServerCredentialUnavailable || server.CredentialState == contract.ServerCredentialLocked || server.CredentialState == contract.ServerCredentialUnsupported || server.CredentialState == contract.ServerCredentialCleanupPending:
			state = "failed"
			detail += "; credential=" + diagnostics.Text(string(server.CredentialState), 80)
			action = "Inspect agent-gateway mcp server get " + server.ID + " and the selected Gateway credential diagnostics; no replacement or retry was attempted."
		case server.CredentialState == contract.ServerCredentialAbsent:
			state = "incomplete"
			action = "Inspect credential requirements with agent-gateway mcp server get " + server.ID
		case server.Runtime.State == contract.RuntimeDegraded || server.Runtime.State == contract.RuntimeRetryWait:
			state = "degraded"
			action = "Inspect agent-gateway mcp server get " + server.ID + " and the selected Gateway logs; doctor did not retry upstream work."
		case server.Runtime.State == contract.RuntimeInactive:
			state = "incomplete"
			action = "Inspect agent-gateway mcp server get " + server.ID + "; activate an intended imported server explicitly with mcp server operation start " + server.ID + " --kind reload --yes"
		}
		if server.Runtime.Reason != nil {
			detail += "; reason=" + diagnostics.Text(string(*server.Runtime.Reason), 80)
		}
		result.Checks = append(result.Checks, doctorCheck{Name: name, State: state, Detail: detail, Next: action})
	}
	result.Checks = append(result.Checks, doctorCheck{Name: "upstream status", State: "ok", Detail: "Bounded server metadata read; no upstream probe, OAuth, refresh, subprocess or tool execution was requested."})
}
