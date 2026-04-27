// Package format provides helpers for formatting GitHub API JSON responses
// into concise, agent-friendly text.
package format

import (
	"fmt"
	"regexp"
	"strings"
)

// Author represents a GitHub user in JSON responses.
type Author struct {
	Login string `json:"login"`
	IsBot bool   `json:"is_bot"`
}

// Label represents a GitHub label.
type Label struct {
	Name string `json:"name"`
}

// FormatAuthor returns "@login" or "@login [bot]".
// If login ends with the literal suffix "[bot]", the suffix is stripped and
// the author is treated as a bot regardless of the IsBot field. This handles
// endpoints (e.g. releases) where is_bot is not populated but the login
// already encodes the bot marker.
func FormatAuthor(a Author) string {
	login := a.Login
	isBot := a.IsBot
	if strings.HasSuffix(login, "[bot]") {
		login = strings.TrimSuffix(login, "[bot]")
		isBot = true
	}
	if login == "" {
		login = "unknown"
	}
	if isBot {
		return fmt.Sprintf("@%s [bot]", login)
	}
	return "@" + login
}

// FormatDate extracts YYYY-MM-DD from an ISO 8601 timestamp.
// Returns ts unchanged if shorter than 10 chars.
func FormatDate(ts string) string {
	if len(ts) < 10 {
		return ts
	}
	return ts[:10]
}

var imageRe = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)

// StripImages replaces markdown image syntax ![alt](url) with [image].
func StripImages(text string) string {
	return imageRe.ReplaceAllString(text, "[image]")
}

// TruncateBody truncates text to maxLen chars on a whitespace boundary.
// If maxLen is 0, returns "". Appends "[truncated -- N/M chars shown]" if cut.
func TruncateBody(text string, maxLen int) string {
	if maxLen == 0 {
		return ""
	}
	if len(text) <= maxLen {
		return text
	}
	// Find last whitespace at or before maxLen.
	cut := maxLen
	if idx := strings.LastIndex(text[:maxLen], " "); idx > 0 {
		cut = idx
	}
	truncated := text[:cut]
	return fmt.Sprintf("%s\n[truncated \u2014 %d/%d chars shown]", truncated, len(truncated), len(text))
}

// DiffFile represents one file's change summary from a unified diff.
type DiffFile struct {
	Path    string
	Added   int
	Removed int
}

// ParseDiffSummary parses a unified diff and returns a markdown file summary table.
// Returns empty string if diff is empty.
func ParseDiffSummary(diff string) string {
	if diff == "" {
		return ""
	}
	lines := strings.Split(diff, "\n")
	var files []DiffFile
	var current *DiffFile
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			// Extract b/path from "diff --git a/path b/path"
			parts := strings.SplitN(line, " b/", 2)
			if len(parts) == 2 {
				files = append(files, DiffFile{Path: parts[1]})
				current = &files[len(files)-1]
			}
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			current.Added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			current.Removed++
		}
	}
	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Files changed (%d)\n\n", len(files))
	sb.WriteString("| File | Changes |\n")
	sb.WriteString("|------|--------|\n")
	for _, f := range files {
		fmt.Fprintf(&sb, "| %s | +%d -%d |\n", f.Path, f.Added, f.Removed)
	}
	return sb.String()
}

// FormatDiff prepends a file summary table to a raw unified diff.
// If maxBytes > 0, the diff body is truncated on a line boundary at maxBytes;
// the summary table is always built from the full diff so the file list stays complete.
func FormatDiff(diff string, maxBytes int) string {
	summary := ParseDiffSummary(diff)
	body := TruncateBytes(diff, maxBytes)
	if summary == "" {
		return body
	}
	return summary + "\n## Diff\n\n" + body
}

// TruncateBytes truncates s to maxBytes on the last newline boundary at or before
// the cap, appending "[truncated — N/M bytes shown]". Returns s unchanged if maxBytes <= 0
// or len(s) <= maxBytes. Used for diff bodies and log tails.
func TruncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	if idx := strings.LastIndexByte(s[:maxBytes], '\n'); idx > 0 {
		cut = idx
	}
	return fmt.Sprintf("%s\n[truncated — %d/%d bytes shown]", s[:cut], cut, len(s))
}

// FormatLabels formats labels as "a, b, c" or "(none)".
func FormatLabels(labels []Label) string {
	if len(labels) == 0 {
		return "(none)"
	}
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return strings.Join(names, ", ")
}
