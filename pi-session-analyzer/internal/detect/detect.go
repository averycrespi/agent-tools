// Package detect implements deterministic structural and heuristic session detectors.
package detect

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
)

type Classification string
type Severity string

const (
	Structural Classification = "structural"
	Heuristic  Classification = "heuristic"
	Error      Severity       = "error"
	Warn       Severity       = "warn"
	Info       Severity       = "info"
)

// Finding is a traceable deterministic diagnostic.
type Finding struct {
	Detector, Summary, EvidenceID, Details string
	Classification                         Classification
	Severity                               Severity
	SourceLine                             int
}

var mcpFailurePattern = regexp.MustCompile(`(?i)\b(?:mcp_call failed|mcp error|fetch failed)\b`)

// Detector is one independently persisted detector.
type Detector struct {
	Name string
	Run  func(ingest.Session) ([]Finding, error)
}

// Registry returns the fixed detector registry in stable order.
func Registry() []Detector {
	return []Detector{
		wrap("broker_guard", brokerGuard),
		wrap("compaction_pressure", compactionPressure),
		wrap("tool_error_burst", toolErrorBurst),
		wrap("mcp_failure", mcpFailure),
		wrap("retry_loop", retryLoop),
		wrap("silent_close", silentClose),
		wrap("unverified_code_change", unverifiedCodeChange),
		wrap("edit_without_read", editWithoutRead),
		wrap("termination", termination),
	}
}

func wrap(name string, run func(ingest.Session) []Finding) Detector {
	return Detector{Name: name, Run: func(session ingest.Session) ([]Finding, error) { return run(session), nil }}
}

// Analyze runs all built-in detectors. It is primarily useful for focused tests.
func Analyze(s ingest.Session) []Finding {
	var out []Finding
	for _, detector := range Registry() {
		findings, _ := detector.Run(s)
		out = append(out, findings...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Detector == out[j].Detector {
			return out[i].EvidenceID < out[j].EvidenceID
		}
		return out[i].Detector < out[j].Detector
	})
	return out
}

func finding(detector string, class Classification, severity Severity, summary, id string, line int, details string) Finding {
	return Finding{Detector: detector, Classification: class, Severity: severity, Summary: summary, EvidenceID: id, SourceLine: line, Details: details}
}

func brokerGuard(s ingest.Session) []Finding {
	groups := map[string][]ingest.CustomMessage{}
	for _, msg := range s.CustomMessages {
		if msg.Type == "broker-guard" {
			groups[msg.Kind] = append(groups[msg.Kind], msg)
		}
	}
	keys := sortedKeys(groups)
	out := []Finding{}
	for _, key := range keys {
		first := groups[key][0]
		out = append(out, finding("broker_guard", Structural, Warn, "broker guard blocked or altered a tool call", first.ID, first.SourceLine, jsonDetails(map[string]any{"kind": key, "count": len(groups[key])})))
	}
	return out
}

func compactionPressure(s ingest.Session) []Finding {
	var events []ingest.Event
	var maxTokens int64
	failed := false
	for _, event := range s.Events {
		if event.Type == "compaction" {
			events = append(events, event)
			if event.TokensBefore > maxTokens {
				maxTokens = event.TokensBefore
			}
			if strings.Contains(strings.ToLower(event.Details), "provider_error") {
				failed = true
			}
		}
	}
	if len(events) == 0 {
		return nil
	}
	severity := Warn
	if failed {
		severity = Error
	}
	return []Finding{finding("compaction_pressure", Structural, severity, "session compacted context", events[0].ID, events[0].SourceLine, jsonDetails(map[string]any{"count": len(events), "max_tokens_before": maxTokens, "provider_failure": failed}))}
}

func toolErrorBurst(s ingest.Session) []Finding {
	total := map[string]int{}
	errors := map[string][]ingest.ToolResult{}
	for _, call := range s.ToolCalls {
		total[call.Name]++
	}
	for _, result := range s.ToolResults {
		if result.IsError != nil && *result.IsError {
			errors[result.Name] = append(errors[result.Name], result)
		}
	}
	out := []Finding{}
	for _, name := range sortedKeys(errors) {
		rs := errors[name]
		if len(rs) >= 3 {
			out = append(out, finding("tool_error_burst", Structural, Warn, "tool returned at least three structural errors", rs[0].ID, rs[0].SourceLine, jsonDetails(map[string]any{"tool": name, "errors": len(rs), "calls": total[name]})))
		}
	}
	return out
}

func mcpFailure(s ingest.Session) []Finding {
	calls := map[string]bool{}
	for _, call := range s.ToolCalls {
		if call.Name == "mcp_call" {
			calls[call.ID] = true
		}
	}
	out := []Finding{}
	for _, result := range s.ToolResults {
		if result.Name != "mcp_call" && !calls[result.CallID] {
			continue
		}
		source := ""
		if result.IsError != nil && *result.IsError {
			source = "structural_flag"
		} else if mcpFailurePattern.MatchString(result.Content) {
			source = "historical_text_fallback"
		}
		if source != "" {
			out = append(out, finding("mcp_failure", Structural, Error, "MCP tool call failed", result.ID, result.SourceLine, jsonDetails(map[string]any{"evidence_source": source})))
		}
	}
	return out
}

func retryLoop(s ingest.Session) []Finding {
	results := map[string]ingest.ToolResult{}
	for _, r := range s.ToolResults {
		results[r.CallID] = r
	}
	type joined struct {
		call   ingest.ToolCall
		result ingest.ToolResult
		ok     bool
	}
	groups := map[string][]joined{}
	for _, call := range sortedCalls(s.ToolCalls) {
		target := normalizedTarget(call)
		key := call.Name + "\x00" + target
		r, ok := results[call.ID]
		groups[key] = append(groups[key], joined{call, r, ok})
	}
	out := []Finding{}
	for _, key := range sortedKeys(groups) {
		entries := groups[key]
		if len(entries) < 4 {
			continue
		}
		last := entries[len(entries)-1]
		if !last.ok || last.result.IsError == nil || !*last.result.IsError {
			continue
		}
		run := 0
		sameHash := 0
		previous := ""
		for _, e := range entries {
			if e.ok && e.result.IsError != nil && *e.result.IsError {
				run++
			} else {
				run = 0
			}
			hash := contentHash(e.result.Content)
			if e.ok && hash == previous {
				sameHash++
			} else {
				sameHash = 1
			}
			previous = hash
		}
		if run >= 3 || sameHash >= 3 {
			out = append(out, finding("retry_loop", Heuristic, Warn, "same tool target repeatedly failed", entries[0].call.ID, entries[0].call.SourceLine, jsonDetails(map[string]any{"invocation_key": key, "calls": len(entries)})))
		}
	}
	return out
}

func silentClose(s ingest.Session) []Finding {
	started := false
	terminal := ""
	evidence := ""
	line := 0
	for _, state := range s.CustomStates {
		if state.Type == "goal-state" {
			started = true
			terminal = state.Status
			evidence = state.ID
			line = state.SourceLine
		}
	}
	if !started || terminal == "complete" {
		return nil
	}
	last := lastAssistant(s.Messages)
	if last == nil || (last.StopReason != "stop" && last.StopReason != "end_turn") {
		return nil
	}
	severity := Error
	summary := "session stopped normally with an incomplete goal"
	for _, result := range s.ToolResults {
		if result.SourceLine > line && result.IsError != nil && *result.IsError {
			severity = Warn
			summary = "failed tool result preceded normal stop"
			evidence = result.ID
			line = result.SourceLine
		}
	}
	return []Finding{finding("silent_close", Heuristic, severity, summary, evidence, line, jsonDetails(map[string]any{"goal_status": terminal}))}
}

var codeExtensions = map[string]bool{".go": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".py": true, ".rb": true, ".rs": true, ".java": true, ".kt": true, ".kts": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".cs": true, ".php": true, ".swift": true, ".scala": true, ".sh": true, ".bash": true, ".zsh": true, ".sql": true, ".proto": true}
var verificationPattern = regexp.MustCompile(`^(?:go\s+(?:test|build|vet)\b|pytest\b|cargo\s+(?:test|check)\b|make\s+(?:test|check|build|lint)\b|npm\s+test\b|(?:npm|pnpm|yarn)\s+run\s+(?:test|check|build|lint|typecheck)\b)`)

func unverifiedCodeChange(s ingest.Session) []Finding {
	var last *ingest.ToolCall
	path := ""
	for i := range s.ToolCalls {
		call := &s.ToolCalls[i]
		if call.Name != "edit" && call.Name != "write" {
			continue
		}
		p := argument(call.Arguments, "path")
		if isCodePath(p) && (last == nil || call.SourceLine > last.SourceLine) {
			last = call
			path = p
		}
	}
	if last == nil {
		return nil
	}
	for _, call := range s.ToolCalls {
		if call.SourceLine > last.SourceLine && call.Name == "bash" && verificationPattern.MatchString(strings.TrimSpace(argument(call.Arguments, "command"))) {
			return nil
		}
	}
	return []Finding{finding("unverified_code_change", Heuristic, Warn, "code changed without a later recognized verification command", last.ID, last.SourceLine, jsonDetails(map[string]any{"path": path}))}
}

func isCodePath(path string) bool {
	clean := filepath.Clean(path)
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "docs" {
			return false
		}
	}
	base := filepath.Base(clean)
	return base == "Makefile" || base == "Dockerfile" || codeExtensions[strings.ToLower(filepath.Ext(base))]
}

func editWithoutRead(s ingest.Session) []Finding {
	seen := map[string]bool{}
	out := []Finding{}
	for _, call := range sortedCalls(s.ToolCalls) {
		path := filepath.Clean(argument(call.Arguments, "path"))
		switch call.Name {
		case "read", "write":
			if path != "." && path != "" {
				seen[path] = true
			}
		case "bash":
			for known := range seen {
				_ = known
			}
			command := argument(call.Arguments, "command")
			for _, later := range s.ToolCalls {
				p := filepath.Clean(argument(later.Arguments, "path"))
				if p != "." && shellReads(command, p) {
					seen[p] = true
				}
			}
		case "edit":
			if path != "." && !seen[path] {
				out = append(out, finding("edit_without_read", Heuristic, Warn, "existing path edited without a prior recognized read", call.ID, call.SourceLine, jsonDetails(map[string]any{"path": path})))
			}
		}
	}
	return out
}

var shellSplit = regexp.MustCompile(`\s*(?:&&|;|\|)\s*`)

func shellReads(command, path string) bool {
	for _, segment := range shellSplit.Split(command, -1) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		name := filepath.Base(strings.Trim(fields[0], `"'`))
		allowed := name == "cat" || name == "head" || name == "tail" || name == "less" || name == "grep" || name == "rg" || name == "awk" || (name == "sed" && len(fields) > 1 && strings.Trim(fields[1], `"'`) == "-n")
		if !allowed {
			continue
		}
		for _, field := range fields[1:] {
			token := strings.Trim(field, `"',`)
			if token == path || token == filepath.Base(path) {
				return true
			}
		}
	}
	return false
}

func termination(s ingest.Session) []Finding {
	last := lastAssistant(s.Messages)
	if last == nil {
		return nil
	}
	switch last.StopReason {
	case "error":
		return []Finding{finding("termination", Heuristic, Error, "provider terminated with an error", last.ID, last.SourceLine, `{"state":"provider_error"}`)}
	case "aborted":
		return []Finding{finding("termination", Heuristic, Info, "session was canceled by the user", last.ID, last.SourceLine, `{"state":"user_aborted"}`)}
	}
	return nil
}

func lastAssistant(messages []ingest.Message) *ingest.Message {
	var last *ingest.Message
	for i := range messages {
		if messages[i].Role == "assistant" && (last == nil || messages[i].SourceLine > last.SourceLine) {
			last = &messages[i]
		}
	}
	return last
}
func normalizedTarget(call ingest.ToolCall) string {
	if call.Name == "bash" {
		return strings.TrimSpace(argument(call.Arguments, "command"))
	}
	if call.Name == "read" || call.Name == "edit" || call.Name == "write" {
		return filepath.Clean(argument(call.Arguments, "path"))
	}
	return strings.TrimSpace(call.Arguments)
}
func argument(raw, key string) string {
	var values map[string]any
	if json.Unmarshal([]byte(raw), &values) != nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}
func sortedCalls(calls []ingest.ToolCall) []ingest.ToolCall {
	out := append([]ingest.ToolCall(nil), calls...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].SourceLine < out[j].SourceLine })
	return out
}
func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func jsonDetails(value any) string    { data, _ := json.Marshal(value); return string(data) }
func contentHash(value string) string { return fmt.Sprintf("%x", []byte(value)) }
