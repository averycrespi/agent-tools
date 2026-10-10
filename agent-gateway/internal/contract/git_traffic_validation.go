package contract

import (
	"encoding/json"
	"slices"
	"strings"
)

// ValidGitRefName validates the deliberately supported ASCII Git ref grammar.
func ValidGitRefName(value string) bool {
	if len(value) > GitRefBytes || !strings.HasPrefix(value, "refs/") || strings.Count(value, "/") < 2 || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e || strings.ContainsRune("~^:?*[\\", rune(b)) {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return false
		}
	}
	return true
}

// ValidGitTrafficRefs accepts legacy absence without reconstructing history.
func ValidGitTrafficRefs(a GitTrafficAdmission) bool {
	e := a.RefEvidence
	if e == nil {
		return true
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > GitTrafficRefEvidenceBytes || a.Operation != "push" || e.Refs == nil || len(e.Refs) > GitTrafficRefs || len(e.Refs) > a.Commands {
		return false
	}
	if e.State != "complete" && e.State != "truncated" || (e.State == "complete") != (len(e.Refs) == a.Commands) {
		return false
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, r := range e.Refs {
		if !ValidGitRefName(r.Name) || seen[r.Name] || !slices.Contains([]string{"create", "update", "delete"}, r.Action) {
			return false
		}
		seen[r.Name] = true
		counts[r.Action]++
	}
	if p := a.Policy; p != nil && (counts["create"] > p.Creates || counts["update"] > p.Updates || counts["delete"] > p.Deletes) {
		return false
	}
	return true
}

func ValidGitTrafficOutcomes(a GitTrafficAdmission, c GitTrafficCompletion) bool {
	if c.RefOutcomes == nil {
		return true
	}
	if a.RefEvidence == nil || len(c.RefOutcomes) == 0 || len(c.RefOutcomes) != len(a.RefEvidence.Refs) || !slices.Contains([]string{"reported_success", "reported_failure", "reported_partial"}, c.ReportedResult) || !a.Allowed || c.Status != 200 || !c.TransferComplete || c.Outcome != "outcome_unknown" {
		return false
	}
	passed := 0
	for _, outcome := range c.RefOutcomes {
		if outcome != "ok" && outcome != "ng" {
			return false
		}
		if outcome == "ok" {
			passed++
		}
	}
	if c.ReportedResult == "reported_success" && passed != len(c.RefOutcomes) || c.ReportedResult == "reported_failure" && passed != 0 {
		return false
	}
	if a.RefEvidence.State == "complete" && c.ReportedResult == "reported_partial" && (passed == 0 || passed == len(c.RefOutcomes)) {
		return false
	}
	return true
}

// GitTrafficOutcome is request outcome, separate from admission and transport.
func GitTrafficOutcome(r GitTrafficRecord) string {
	a, c := r.Admission, r.Completion
	if !a.Allowed {
		return "Denied"
	}
	if c == nil {
		return "Unknown"
	}
	if c.Outcome == "prestart_failure" || c.Status >= 400 {
		return "Failed"
	}
	if !c.TransferComplete {
		return "Incomplete"
	}
	if a.Operation == "push" {
		switch c.ReportedResult {
		case "reported_success":
			return "Reported success"
		case "reported_failure":
			return "Reported failure"
		case "reported_partial":
			return "Reported partial success"
		default:
			return "Unknown"
		}
	}
	if c.Status >= 200 && c.Status < 300 {
		return "HTTP success"
	}
	return "Unknown"
}
