package catalog

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

var (
	syntheticOnce        sync.Once
	syntheticDescriptors []contract.ToolDescriptor
	syntheticValidators  []*InputValidator
	syntheticByName      map[string]int
	syntheticErr         error
)

type SyntheticCallTarget struct {
	Descriptor contract.ToolDescriptor
	Validator  *InputValidator
}

func SyntheticSnapshot() ([]contract.ToolDescriptor, error) {
	syntheticOnce.Do(compileSyntheticSnapshot)
	if syntheticErr != nil {
		return nil, syntheticErr
	}
	result := make([]contract.ToolDescriptor, len(syntheticDescriptors))
	for index := range syntheticDescriptors {
		result[index] = cloneToolDescriptor(syntheticDescriptors[index])
	}
	return result, nil
}

func ResolveSyntheticCall(externalName string) (SyntheticCallTarget, bool) {
	syntheticOnce.Do(compileSyntheticSnapshot)
	index, found := syntheticByName[externalName]
	if syntheticErr != nil || !found {
		return SyntheticCallTarget{}, false
	}
	return SyntheticCallTarget{Descriptor: cloneToolDescriptor(syntheticDescriptors[index]), Validator: syntheticValidators[index]}, true
}

func compileSyntheticSnapshot() {
	tools := contract.SyntheticSelfServiceTools()
	if len(tools) != int(fixedLimit("discoverable_tools")-fixedLimit("active_tools")) {
		syntheticErr = fmt.Errorf("%w: synthetic tool count", ErrDescriptorInvalid)
		return
	}
	syntheticDescriptors = make([]contract.ToolDescriptor, len(tools))
	syntheticValidators = make([]*InputValidator, len(tools))
	syntheticByName = make(map[string]int, len(tools))
	for index, tool := range tools {
		normalized, err := NormalizeTool(RawTool{
			UpstreamName: tool.UpstreamName,
			ExternalName: tool.ExternalName,
			Descriptor:   tool.Canonical,
		}, NormalizeOptions{ServerID: contract.SyntheticServerID})
		validator, validatorErr := compileInputValidator(tool.Descriptor.InputSchema)
		_, duplicate := syntheticByName[tool.ExternalName]
		if err != nil || validatorErr != nil || duplicate || normalized.Key.ServerID != tool.ServerID || normalized.Key.UpstreamName != tool.UpstreamName ||
			normalized.ExternalName != tool.ExternalName || normalized.Fingerprint != tool.Fingerprint ||
			!bytes.Equal(normalized.Canonical, tool.Canonical) {
			syntheticDescriptors = nil
			syntheticValidators = nil
			syntheticByName = nil
			syntheticErr = fmt.Errorf("%w: synthetic tool %d", ErrDescriptorInvalid, index)
			return
		}
		syntheticDescriptors[index] = contract.ToolDescriptor{
			ID: tool.ID, ServerID: tool.ServerID, UpstreamName: tool.UpstreamName, ExternalName: tool.ExternalName,
			Descriptor: tool.Descriptor, Fingerprint: normalized.Fingerprint, CatalogRevision: tool.CatalogRevision,
		}
		syntheticValidators[index] = validator
		syntheticByName[tool.ExternalName] = index
	}
}

func cloneToolDescriptor(source contract.ToolDescriptor) contract.ToolDescriptor {
	result := source
	result.Descriptor.Title = cloneActiveString(source.Descriptor.Title)
	result.Descriptor.Description = cloneActiveString(source.Descriptor.Description)
	result.Descriptor.InputSchema = bytes.Clone(source.Descriptor.InputSchema)
	result.Descriptor.OutputSchema = bytes.Clone(source.Descriptor.OutputSchema)
	result.Descriptor.Annotations.Title = cloneActiveString(source.Descriptor.Annotations.Title)
	result.RetiredAt = cloneActiveString(source.RetiredAt)
	return result
}
