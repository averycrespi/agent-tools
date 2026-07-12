// Package detect implements deterministic structural and heuristic session detectors.
package detect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/outcome"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/scrub"
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
	callNames := map[string]string{}
	for _, call := range s.ToolCalls {
		total[call.Name]++
		callNames[call.ID] = call.Name
	}
	type burst struct {
		count int
		first ingest.ToolResult
	}
	bursts := map[string]burst{}
	for _, result := range sortedResults(s.ToolResults) {
		name := result.Name
		if name == "" {
			name = callNames[result.CallID]
		}
		current := bursts[name]
		if result.IsError != nil {
			if *result.IsError {
				if current.count == 0 {
					current.first = result
				}
				current.count++
			} else {
				current = burst{}
			}
		}
		bursts[name] = current
	}
	out := []Finding{}
	for _, name := range sortedKeys(bursts) {
		current := bursts[name]
		if current.count >= 3 {
			out = append(out, finding("tool_error_burst", Structural, Warn, "tool ended with at least three unrecovered structural errors", current.first.ID, current.first.SourceLine, jsonDetails(map[string]any{"tool": name, "errors": current.count, "calls": total[name]})))
		}
	}
	return out
}

func mcpFailure(s ingest.Session) []Finding {
	calls := map[string]string{}
	for _, call := range s.ToolCalls {
		calls[call.ID] = call.Name
	}
	out := []Finding{}
	for _, result := range s.ToolResults {
		callName := calls[result.CallID]
		if result.Name != "mcp_call" && callName != "mcp_call" {
			continue
		}
		source := ""
		if result.IsError != nil && *result.IsError {
			source = "structural_flag"
		} else if outcome.IsInferredMCPFailure(callName, result.Name, result.IsError, result.Content) {
			source = "historical_text_fallback"
		}
		if source != "" {
			out = append(out, finding("mcp_failure", Structural, Error, "MCP tool call failed", result.ID, result.SourceLine, jsonDetails(map[string]any{"evidence_source": source})))
		}
	}
	return out
}

var historicalFailurePattern = regexp.MustCompile(`(?i)\b(?:error|fail(?:ed|ure)?|denied|timeout|timed out|not found)\b`)

func retryLoop(s ingest.Session) []Finding {
	results := map[string]ingest.ToolResult{}
	for _, result := range s.ToolResults {
		results[result.CallID] = result
	}
	calls := sortedCalls(s.ToolCalls)
	counts := map[string]int{}
	for _, call := range calls {
		counts[call.Name+"\x00"+normalizedTarget(call)]++
	}
	found := map[string]ingest.ToolCall{}
	currentKey, previousHash := "", ""
	structuralErrors, matchingFailures := 0, 0
	var first ingest.ToolCall
	flush := func() {
		if currentKey != "" && counts[currentKey] >= 4 && (structuralErrors >= 3 || matchingFailures >= 3) {
			if _, exists := found[currentKey]; !exists {
				found[currentKey] = first
			}
		}
	}
	for _, call := range calls {
		key := call.Name + "\x00" + normalizedTarget(call)
		if key != currentKey {
			flush()
			currentKey, previousHash = key, ""
			structuralErrors, matchingFailures = 0, 0
			first = call
		}
		result, ok := results[call.ID]
		if ok && result.IsError != nil && *result.IsError {
			structuralErrors++
		} else {
			structuralErrors = 0
		}
		if ok && result.IsError == nil && historicalFailurePattern.MatchString(result.Content) {
			hash := contentHash(scrub.Scrub(result.Content))
			if hash == previousHash {
				matchingFailures++
			} else {
				matchingFailures = 1
			}
			previousHash = hash
		} else {
			matchingFailures, previousHash = 0, ""
		}
	}
	flush()
	out := []Finding{}
	for _, key := range sortedKeys(found) {
		evidence := found[key]
		out = append(out, finding("retry_loop", Heuristic, Warn, "same tool target repeatedly failed", evidence.ID, evidence.SourceLine, jsonDetails(map[string]any{"invocation_key": key, "calls": counts[key]})))
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
	if !started || terminal == "" || terminal == "complete" {
		return nil
	}
	last := lastAssistant(s.Messages)
	if last == nil || (last.StopReason != "stop" && last.StopReason != "end_turn") {
		return nil
	}
	severity := Error
	summary := "session stopped normally with an incomplete goal"
	var finalResult *ingest.ToolResult
	for i := range s.ToolResults {
		result := &s.ToolResults[i]
		if result.SourceLine > line && (finalResult == nil || result.SourceLine > finalResult.SourceLine) {
			finalResult = result
		}
	}
	if finalResult != nil && finalResult.IsError != nil && *finalResult.IsError {
		severity = Warn
		summary = "final failed tool result preceded normal stop"
		evidence = finalResult.ID
		line = finalResult.SourceLine
	}
	return []Finding{finding("silent_close", Heuristic, severity, summary, evidence, line, jsonDetails(map[string]any{"goal_status": terminal}))}
}

var codeExtensions = map[string]bool{".go": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".py": true, ".rb": true, ".rs": true, ".java": true, ".kt": true, ".kts": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".cs": true, ".php": true, ".swift": true, ".scala": true, ".sh": true, ".bash": true, ".zsh": true, ".sql": true, ".proto": true}
var verificationPattern = regexp.MustCompile(`^(?:go(?:\s+-C\s+\S+)?\s+(?:test|build|vet|fmt|run)\b|gofmt\b|pytest\b|cargo\s+(?:test|check)\b|make(?:\s+-C\s+\S+)?\s+(?:test|check|build|lint)\b|(?:npm|pnpm|yarn)(?:\s+(?:--prefix|--dir|--cwd|-C)\s+\S+)*\s+(?:test\b|run\s+(?:test|check|build|lint|typecheck)\b)|(?:uv|poetry)\s+run\s+pytest\b|npx(?:\s+(?:(?:--prefix|--dir|--cwd|-C)\s+\S+|--yes))*\s+(?:vitest|jest|mocha)\b|npx(?:\s+(?:(?:--prefix|--dir|--cwd|-C)\s+\S+|--yes))*\s+tsx\s+(?:--test\b|\S+\.tsx?\b)|npx\s+tsc\b.*--noEmit\b|(?:bash|sh)\s+-n\b|shellcheck\b|golangci-lint\s+run\b)`)
var leadingEnvPattern = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*=\S+\s+)+`)

func unverifiedCodeChange(s ingest.Session) []Finding {
	failed := map[string]bool{}
	for _, result := range s.ToolResults {
		failed[result.CallID] = result.IsError != nil && *result.IsError
	}
	var last *ingest.ToolCall
	path := ""
	for i := range s.ToolCalls {
		call := &s.ToolCalls[i]
		if (call.Name != "edit" && call.Name != "write") || failed[call.ID] {
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
		if call.SourceLine > last.SourceLine && call.Name == "bash" && isVerificationCommand(argument(call.Arguments, "command")) {
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
	shellReadTargets := map[string]bool{}
	shellReadPatterns := map[string]bool{}
	readTaskIDs := map[string]bool{}
	failed := map[string]bool{}
	for _, result := range s.ToolResults {
		failed[result.CallID] = result.IsError != nil && *result.IsError
	}
	out := []Finding{}
	for _, call := range sortedCalls(s.ToolCalls) {
		rawPath := argument(call.Arguments, "path")
		path := canonicalPath(s.CWD, rawPath)
		switch call.Name {
		case "read":
			if path != "." && path != "" {
				seen[path] = true
			}
		case "write":
			if path != "." && path != "" && !failed[call.ID] {
				seen[path] = true
			}
		case "scheduled_tasks":
			if argument(call.Arguments, "action") == "read" {
				readTaskIDs[argument(call.Arguments, "task_id")] = true
			}
		case "bash":
			command := argument(call.Arguments, "command")
			for _, target := range shellReadOperands(command, s.CWD) {
				if strings.ContainsAny(target, "*?[") {
					shellReadPatterns[target] = true
				} else {
					shellReadTargets[target] = true
					shellReadTargets[filepath.Base(target)] = true
				}
			}
			for _, moved := range shellMoves(command, s.CWD) {
				rebaseReadPaths(seen, moved[0], moved[1])
				rebaseReadPaths(shellReadTargets, moved[0], moved[1])
				rebaseReadPaths(shellReadPatterns, moved[0], moved[1])
			}
		case "edit":
			taskRead := filepath.Base(filepath.Dir(path)) == "tasks" && readTaskIDs[strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))]
			read := seen[path] || shellReadTargets[path] || shellReadTargets[filepath.Base(path)] || matchesReadPattern(shellReadPatterns, path) || taskRead
			if path != "." && !read && !failed[call.ID] {
				out = append(out, finding("edit_without_read", Heuristic, Warn, "existing path edited without a prior recognized read", call.ID, call.SourceLine, jsonDetails(map[string]any{"path": rawPath})))
			}
			if path != "." && !failed[call.ID] {
				seen[path] = true
			}
		}
	}
	return out
}

var shellSplit = regexp.MustCompile(`\s*(?:&&|;|\|)\s*`)

func isVerificationCommand(command string) bool {
	for _, segment := range shellSplit.Split(command, -1) {
		segment = normalizeVerificationSegment(segment)
		if verificationPattern.MatchString(segment) {
			return true
		}
	}
	return false
}

func normalizeVerificationSegment(segment string) string {
	segment = leadingEnvPattern.ReplaceAllString(strings.TrimSpace(segment), "")
	fields := shellFields(segment)
	if len(fields) == 0 || fields[0] != "env" {
		return segment
	}
	i := 1
	for i < len(fields) {
		switch {
		case fields[i] == "--":
			i++
			return strings.Join(fields[i:], " ")
		case fields[i] == "-u" || fields[i] == "--unset":
			i += 2
		case strings.HasPrefix(fields[i], "-") || strings.Contains(fields[i], "="):
			i++
		default:
			return strings.Join(fields[i:], " ")
		}
	}
	return ""
}

func shellReadOperands(command, cwd string) []string {
	var targets []string
	for _, segment := range shellSplit.Split(command, -1) {
		fields := shellFields(segment)
		if len(fields) == 0 {
			continue
		}
		reader := -1
		for i, field := range fields {
			name := filepath.Base(strings.Trim(field, `"'(){}`))
			previous := ""
			if i > 0 {
				previous = strings.Trim(fields[i-1], `"'(){}`)
			}
			atCommandStart := i == 0 || previous == "then" || previous == "do"
			allowed := name == "cat" || name == "head" || name == "tail" || name == "less" || name == "grep" || name == "rg" || name == "awk" || name == "nl" || (name == "sed" && len(fields) > i+1 && strings.Trim(fields[i+1], `"'`) == "-n")
			if atCommandStart && allowed {
				reader = i
				break
			}
		}
		if reader < 0 && len(fields) >= 2 && filepath.Base(fields[0]) == "git" && (fields[1] == "diff" || fields[1] == "show") {
			for i, field := range fields {
				if field == "--" {
					reader = i
					break
				}
			}
		}
		if reader < 0 {
			continue
		}
		operands := make([]string, 0, len(fields)-reader-1)
		for _, field := range fields[reader+1:] {
			token := strings.Trim(field, `"',`)
			if token != "" && !strings.HasPrefix(token, "-") {
				operands = append(operands, token)
			}
		}
		readerName := filepath.Base(strings.Trim(fields[reader], `"'(){}`))
		if (readerName == "grep" || readerName == "rg" || readerName == "awk" || readerName == "sed") && len(operands) > 0 {
			operands = operands[1:]
		}
		for _, operand := range operands {
			targets = append(targets, canonicalPath(cwd, operand))
		}
	}
	return targets
}

func rebaseReadPaths(paths map[string]bool, source, destination string) {
	for path := range paths {
		relative, err := filepath.Rel(source, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			paths[filepath.Join(destination, relative)] = true
		}
	}
}

func matchesReadPattern(patterns map[string]bool, path string) bool {
	for pattern := range patterns {
		if matched, err := filepath.Match(pattern, path); err == nil && matched {
			return true
		}
	}
	return false
}

func shellMoves(command, cwd string) [][2]string {
	var moves [][2]string
	for _, segment := range shellSplit.Split(command, -1) {
		fields := shellFields(segment)
		if len(fields) == 3 && filepath.Base(fields[0]) == "mv" {
			moves = append(moves, [2]string{canonicalPath(cwd, fields[1]), canonicalPath(cwd, fields[2])})
		}
	}
	return moves
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
func canonicalPath(cwd, path string) string {
	clean := filepath.Clean(path)
	if cwd != "" && clean != "." && !filepath.IsAbs(clean) {
		return filepath.Join(cwd, clean)
	}
	return clean
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

func sortedResults(results []ingest.ToolResult) []ingest.ToolResult {
	out := append([]ingest.ToolResult(nil), results...)
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
func contentHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func shellFields(command string) []string {
	var fields []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	flush()
	return fields
}
