package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// maxNormalizedTargetBytes bounds the persisted derived target so downstream
// queries can rely on finite values without per-query truncation.
const maxNormalizedTargetBytes = 512

// normalizeCallTarget derives the aggregation target for one tool call from its
// already-scrubbed arguments JSON: the cleaned path argument for read/edit/write,
// the leading command word (after env assignments) for bash, and "" otherwise.
func normalizeCallTarget(name, arguments string) string {
	var target string
	switch name {
	case "read", "edit", "write":
		path := jsonStringField(arguments, "path")
		if path == "" {
			return ""
		}
		target = filepath.Clean(path)
	case "bash":
		target = leadingCommandWord(jsonStringField(arguments, "command"))
	default:
		return ""
	}
	if len(target) > maxNormalizedTargetBytes {
		target = target[:maxNormalizedTargetBytes]
	}
	return target
}

func jsonStringField(raw, key string) string {
	var values map[string]any
	if json.Unmarshal([]byte(raw), &values) != nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}

func leadingCommandWord(command string) string {
	for _, field := range strings.Fields(command) {
		if isEnvAssignment(field) {
			continue
		}
		return field
	}
	return ""
}

func isEnvAssignment(field string) bool {
	name, _, found := strings.Cut(field, "=")
	if !found || name == "" {
		return false
	}
	for i, r := range name {
		alpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		if !alpha && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// SkillFileBasename is the conventional per-skill instruction file. A read tool
// call whose normalized target ends in /SkillFileBasename is a skill invocation,
// and the parent directory name is the skill identity.
const SkillFileBasename = "SKILL.md"

// SkillNameFromTarget returns the skill directory name for a SKILL.md target,
// or "" when the target is not a skill instruction path.
func SkillNameFromTarget(target string) string {
	if filepath.Base(target) != SkillFileBasename {
		return ""
	}
	name := filepath.Base(filepath.Dir(target))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}
