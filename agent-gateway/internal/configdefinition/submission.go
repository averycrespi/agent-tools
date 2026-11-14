package configdefinition

import (
	"encoding/json"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// DecodeSubmission accepts the identical individual envelopes used by export,
// or a partial bundle. Dependency resolution requires a receiving snapshot.
func DecodeSubmission(raw []byte) ([]contract.ConfigDefinition, error) {
	if len(raw) == 0 || len(raw) > contract.ConfigExportMaxBytes {
		return nil, ErrInvalid
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil {
		return nil, ErrInvalid
	}
	var definitions []contract.ConfigDefinition
	if _, bundle := shape["resources"]; bundle {
		var b contract.ConfigBundle
		if decode(raw, &b, contract.ConfigExportMaxBytes) != nil || b.Format != contract.ConfigExportFormat || b.Version != contract.ConfigV2Version || b.Resources == nil {
			return nil, ErrInvalid
		}
		definitions = b.Resources
	} else {
		var d contract.ConfigDefinition
		if decode(raw, &d, contract.ConfigExportMaxBytes) != nil {
			return nil, ErrInvalid
		}
		definitions = []contract.ConfigDefinition{d}
	}
	if len(definitions) > MaxKeyBindings+1025 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, d := range definitions {
		if _, err := Normalize(d); err != nil {
			return nil, ErrInvalid
		}
		id := identity(d.Kind, d.Key)
		if seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
	}
	return definitions, nil
}
