package gitpolicy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestTrafficRefBounds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		count, length int
	}{{"complete", 3, 10}, {"count", 128, 10}, {"bytes", 8, 1000}, {"escaping", 8, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			actions := make([]RefAction, tc.count)
			for i := range actions {
				suffix := strings.Repeat("a", tc.length)
				if tc.name == "escaping" {
					suffix = strings.Repeat("&", tc.length)
				}
				actions[i] = RefAction{Ref: fmt.Sprintf("refs/heads/%d%s", i, suffix), Action: []string{"create", "update", "delete"}[i%3]}
			}
			e := TrafficRefs(actions)
			raw, err := json.Marshal(e)
			require.NoError(t, err)
			require.LessOrEqual(t, len(raw), contract.GitTrafficRefEvidenceBytes)
			require.LessOrEqual(t, len(e.Refs), contract.GitTrafficRefs)
			require.True(t, contract.ValidGitTrafficRefs(contract.GitTrafficAdmission{Operation: "push", Commands: tc.count, RefEvidence: e}))
			if tc.name == "complete" {
				require.Equal(t, "complete", e.State)
			} else {
				require.Equal(t, "truncated", e.State)
				require.Less(t, len(e.Refs), tc.count)
			}
			for i, r := range e.Refs {
				require.Equal(t, actions[i].Ref, r.Name)
				require.Equal(t, actions[i].Action, r.Action)
			}
		})
	}
	require.Nil(t, TrafficRefs(nil))
}
