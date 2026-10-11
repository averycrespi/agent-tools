package gitpolicy

import (
	"encoding/json"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// TrafficRefs deliberately projects only bounded names/actions, never OIDs.
func TrafficRefs(actions []RefAction) *contract.GitTrafficRefEvidence {
	if len(actions) == 0 {
		return nil
	}
	e := &contract.GitTrafficRefEvidence{State: "truncated", Refs: []contract.GitTrafficRequestedRef{}}
	for _, a := range actions {
		if len(e.Refs) == contract.GitTrafficRefs {
			break
		}
		e.Refs = append(e.Refs, contract.GitTrafficRequestedRef{Name: a.Ref, Action: a.Action})
		raw, _ := json.Marshal(e)
		if len(raw) > contract.GitTrafficRefEvidenceBytes {
			e.Refs = e.Refs[:len(e.Refs)-1]
			break
		}
	}
	if len(e.Refs) == len(actions) {
		e.State = "complete"
	}
	return e
}
