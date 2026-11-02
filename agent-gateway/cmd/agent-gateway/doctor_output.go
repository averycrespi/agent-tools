package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

func renderDoctorChecks(result doctorResult, verbose bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Data directory: %s (%s)\n", controlclient.TerminalSafePath(result.DataDir), result.Selection)
	if result.Result != "" {
		fmt.Fprintf(&out, "Diagnosis: %s\n", result.Result)
	}
	out.WriteString("\n")
	labels := map[string]string{
		"installation": "Installation", "storage": "Database", "process ownership": "Gateway",
		"administrator credential": "Administrator", "public CA certificate": "CA certificate",
		"protected CA signing material": "CA signing", "storage inspection": "Storage integrity",
		"CA metadata": "CA metadata", "runtime readiness": "API readiness",
		"authenticated live status": "Live status",
	}
	states := map[string]string{"ok": "OK", "present": "Present", "absent": "Missing", "running": "Running", "stopped": "Stopped", "not-checked": "Not checked", "failed": "Failed", "unavailable": "Unavailable", "not-ready": "Not ready"}
	shownActions := make(map[string]bool)
	checks := append([]doctorCheck(nil), result.Checks...)
	priority := func(c doctorCheck) int {
		if c.Name == "process ownership" || c.Name == "runtime readiness" || c.Name == "authenticated live status" {
			return 0
		}
		if doctorNeedsAttention(c) {
			return 1
		}
		return 2
	}
	sort.SliceStable(checks, func(i, j int) bool { return priority(checks[i]) < priority(checks[j]) })
	for _, check := range checks {
		if !verbose && priority(check) == 2 {
			continue
		}
		label, state := labels[check.Name], states[check.State]
		if label == "" {
			label = check.Name
		}
		if state == "" {
			state = check.State
		}
		if check.Name == "runtime readiness" && check.State == "ok" {
			state = "Ready"
		}
		path := check.Path
		if path != "" {
			relative, err := filepath.Rel(result.DataDir, path)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				path = relative
				if path == "." {
					path = ""
				}
			}
		}
		line := fmt.Sprintf("%-18s %-12s %s", diagnostics.Text(label, 160), diagnostics.Text(state, 40), controlclient.TerminalSafePath(path))
		out.WriteString(strings.TrimRight(line, " ") + "\n")
		if verbose || doctorNeedsAttention(check) {
			fmt.Fprintf(&out, "  %s\n", diagnostics.Text(check.Detail, 2048))
		}
		if check.Next != "" && !shownActions[check.Next] {
			switch check.State {
			case "ok", "present", "running", "stopped":
				if verbose {
					fmt.Fprintf(&out, "  %s\n", diagnostics.Text(check.Next, 2048))
				}
			default:
				fmt.Fprintf(&out, "  Next: %s\n", diagnostics.Text(check.Next, 2048))
				shownActions[check.Next] = true
			}
		}
	}
	return out.String()
}

func doctorNeedsAttention(c doctorCheck) bool {
	switch c.State {
	case "failed", "not-ready", "unavailable", "absent", "incomplete", "degraded":
		return true
	default:
		return c.Next != "" && c.State != "ok" && c.State != "present" && c.State != "running" && c.State != "stopped"
	}
}
