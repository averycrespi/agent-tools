package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
)

func renderDoctorChecks(result doctorResult, verbose bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Data directory: %s (%s)\n\n", controlclient.TerminalSafePath(result.DataDir), result.Selection)
	labels := map[string]string{
		"installation": "Installation", "storage": "Database", "process ownership": "Gateway",
		"administrator credential": "Administrator", "public CA certificate": "CA certificate",
		"protected CA signing material": "CA signing", "storage inspection": "Storage integrity",
		"CA metadata": "CA metadata", "runtime readiness": "API readiness",
		"installed service": "Service", "authenticated live status": "Live status",
	}
	states := map[string]string{"ok": "OK", "present": "Present", "absent": "Missing", "running": "Running", "stopped": "Stopped", "not-checked": "Not checked", "failed": "Failed", "unavailable": "Unavailable", "not-ready": "Not ready"}
	shownActions := make(map[string]bool)
	for _, check := range result.Checks {
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
		line := fmt.Sprintf("%-18s %-12s %s", label, state, controlclient.TerminalSafePath(path))
		out.WriteString(strings.TrimRight(line, " ") + "\n")
		if verbose || check.State == "failed" || check.State == "not-ready" {
			fmt.Fprintf(&out, "  %s\n", check.Detail)
		}
		if check.Next != "" && !shownActions[check.Next] {
			switch check.State {
			case "ok", "present", "running", "stopped":
				if verbose {
					fmt.Fprintf(&out, "  %s\n", check.Next)
				}
			default:
				fmt.Fprintf(&out, "  Next: %s\n", check.Next)
				shownActions[check.Next] = true
			}
		}
	}
	if s := result.Service; s != nil && s.Installed {
		out.WriteString("\nService paths\n")
		for _, field := range [][2]string{{"Configuration", s.Plist}, {"Stdout log", s.Stdout}, {"Stderr log", s.Stderr}} {
			if field[1] != "" {
				fmt.Fprintf(&out, "  %-16s %s\n", field[0], controlclient.TerminalSafePath(field[1]))
			}
		}
	}
	return out.String()
}
