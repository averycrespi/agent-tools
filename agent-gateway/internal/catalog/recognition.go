package catalog

import (
	"slices"
	"strings"
	"unicode"
)

// MatchInventoryIdentity recognizes current names with the catalog's existing
// normalization. Identifiers are literal substrings, never normalized or repaired.
func MatchInventoryIdentity(name, id, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" || id != "" && strings.Contains(id, query) {
		return true
	}
	candidate := toolRecognition(name)
	words := strings.FieldsFunc(candidate, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for _, token := range strings.Fields(toolRecognition(query)) {
		if strings.Contains(candidate, token) {
			continue
		}
		if len([]rune(token)) < 4 || strings.IndexFunc(token, unicode.IsDigit) >= 0 || !slices.ContainsFunc(words, func(word string) bool { return inventoryOneEdit([]rune(word), []rune(token)) }) {
			return false
		}
	}
	return true
}

func inventoryOneEdit(a, b []rune) bool {
	if len(a)-len(b) > 1 || len(b)-len(a) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		switch {
		case len(a) == len(b) && i+1 < len(a) && j+1 < len(b) && a[i] == b[j+1] && a[i+1] == b[j]:
			i += 2
			j += 2
		case len(a) > len(b):
			i++
		case len(b) > len(a):
			j++
		default:
			i++
			j++
		}
	}
	return edits+len(a)-i+len(b)-j <= 1
}
