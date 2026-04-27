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

// htmlCommentRe matches HTML comments including multi-line content. The `(?s)`
// flag makes `.` match newlines so comments spanning multiple lines collapse.
var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// StripHTMLComments removes `<!-- ... -->` blocks from text. Useful for body
// excerpts where PR/issue templates leak template instructions ("<!-- Provide
// a short description of your PR -->") that have no value in a one-line view.
func StripHTMLComments(text string) string {
	return htmlCommentRe.ReplaceAllString(text, "")
}

// reviewHTMLRe matches the small set of HTML tags GitHub's renderer injects
// into PR review bodies — most commonly `<details>` / `<summary>` blocks and
// `<a class="Link--inTextBlock">…</a>` link wrappers. Other tags pass through
// because they may be intentional (e.g. `<code>`, `<sub>`, nested formatting).
// `(?:\s[^>]*)?` keeps the regex from matching `<address>` etc.
var reviewHTMLRe = regexp.MustCompile(`</?(?:details|summary|a)(?:\s[^>]*)?>`)

// StripReviewHTML removes `<details>` / `<summary>` / `<a>` open and close
// tags from text but preserves the inner content. This is targeted at the
// HTML noise that appears in automated PR reviews (Copilot in particular);
// human-written markdown rarely uses these tags directly.
func StripReviewHTML(text string) string {
	return reviewHTMLRe.ReplaceAllString(text, "")
}

// TruncateBody truncates text to maxLen bytes on a whitespace boundary.
// If maxLen is 0, returns "". Appends "[truncated \u2014 showing X of Y bytes]" if cut.
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
	return fmt.Sprintf("%s\n[truncated \u2014 showing %d of %d bytes]", truncated, len(truncated), len(text))
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
// the cap, appending "[truncated — showing X of Y bytes]". Returns s unchanged if maxBytes <= 0
// or len(s) <= maxBytes. Used for diff bodies where the START of the content matters most.
func TruncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	if idx := strings.LastIndexByte(s[:maxBytes], '\n'); idx > 0 {
		cut = idx
	}
	return fmt.Sprintf("%s\n[truncated — showing %d of %d bytes]", s[:cut], cut, len(s))
}

// TruncateLogTail truncates s to keep the LAST maxBytes bytes on a line
// boundary, prepending "[truncated — showing last X of Y bytes]". Returns s
// unchanged if maxBytes <= 0 or len(s) <= maxBytes. The trim direction is
// inverted relative to TruncateBytes because for log triage the most-recent
// lines (typically containing the actual error) are what matters; trimming
// from the end would drop them.
func TruncateLogTail(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := len(s) - maxBytes
	// Advance to the next newline so we don't break a partial line at the start.
	if idx := strings.IndexByte(s[cut:], '\n'); idx >= 0 {
		cut += idx + 1
	}
	kept := len(s) - cut
	return fmt.Sprintf("[truncated — showing last %d of %d bytes]\n%s", kept, len(s), s[cut:])
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
