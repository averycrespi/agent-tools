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
func FormatAuthor(a Author) string {
	login := a.Login
	if login == "" {
		login = "unknown"
	}
	if a.IsBot {
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
