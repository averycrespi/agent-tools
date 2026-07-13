package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCallTargetDerivesPathAndCommandTargets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, tool, arguments, want string
	}{
		{"read path is cleaned", "read", `{"path":"/repo/./skills/tdd/SKILL.md"}`, "/repo/skills/tdd/SKILL.md"},
		{"edit path", "edit", `{"path":"main.go","edits":[]}`, "main.go"},
		{"write path", "write", `{"path":"docs/out.md","content":"x"}`, "docs/out.md"},
		{"read without path", "read", `{}`, ""},
		{"bash leading word", "bash", `{"command":"go test ./..."}`, "go"},
		{"bash skips env assignments", "bash", `{"command":"FOO=1 BAR_2=x make lint"}`, "make"},
		{"bash empty command", "bash", `{"command":"   "}`, ""},
		{"non-target tool", "todo", `{"path":"/x"}`, ""},
		{"invalid json", "read", `not-json`, ""},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, normalizeCallTarget(tc.tool, tc.arguments), tc.name)
	}

	long := `{"path":"/` + strings.Repeat("a", 2*maxNormalizedTargetBytes) + `"}`
	require.Len(t, normalizeCallTarget("read", long), maxNormalizedTargetBytes)
}

func TestSkillNameFromTargetRequiresSkillFileWithParent(t *testing.T) {
	t.Parallel()

	require.Equal(t, "tdd", SkillNameFromTarget("/home/u/.pi/agent/skills/tdd/SKILL.md"))
	require.Equal(t, "review", SkillNameFromTarget("skills/review/SKILL.md"))
	require.Empty(t, SkillNameFromTarget("/repo/README.md"))
	require.Empty(t, SkillNameFromTarget("SKILL.md"))
	require.Empty(t, SkillNameFromTarget("/SKILL.md"))
	require.Empty(t, SkillNameFromTarget(""))
}
