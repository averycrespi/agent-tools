package naming

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type Item struct {
	Identity string
	Label    string
}

var repeatedDash = regexp.MustCompile(`-+`)

func Slug(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	result := strings.Trim(repeatedDash.ReplaceAllString(b.String(), "-"), "-_")
	if result == "" {
		return "worktree"
	}
	if len(result) > 48 {
		result = strings.TrimRight(result[:48], "-_")
	}
	return result
}

func short(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", sum[:4])
}

func Windows(items []Item) map[string]string {
	baseByID := make(map[string]string, len(items))
	counts := make(map[string]int)
	for _, item := range items {
		base := Slug(item.Label)
		baseByID[item.Identity] = base
		counts[base]++
	}
	ids := make([]string, 0, len(baseByID))
	for id := range baseByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make(map[string]string, len(items))
	used := make(map[string]bool)
	for _, id := range ids {
		base := baseByID[id]
		name := base
		if counts[base] > 1 || used[name] {
			name = base + "-" + short(id)
		}
		for used[name] {
			name += "-" + short(id+name)
		}
		used[name] = true
		out[id] = name
	}
	return out
}

func Detached(head, path string) string {
	if len(head) >= 8 {
		return "detached-" + strings.ToLower(head[:8])
	}
	return "detached-" + short(filepath.Clean(path))
}

func Path(root, repoID, branch, identity string, occupied func(string) bool) string {
	base := filepath.Join(root, repoID, Slug(branch))
	if !occupied(base) {
		return base
	}
	for collision := 0; ; collision++ {
		candidate := base + "-" + short(fmt.Sprintf("%s:%d", identity, collision))
		if !occupied(candidate) {
			return candidate
		}
	}
}
