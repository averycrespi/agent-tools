package catalog

import (
	"encoding/json"
	"errors"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var ErrArgumentsInvalid = errors.New("tool arguments are invalid")

type InputValidator struct {
	schema *jsonschema.Schema
}

func compileInputValidator(raw json.RawMessage) (*InputValidator, error) {
	var schemaValue any
	if err := decodeNumbered(raw, &schemaValue); err != nil {
		return nil, ErrDescriptorInvalid
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(rejectingLoader{})
	const location = "urn:mcp-gateway:input-schema"
	if err := compiler.AddResource(location, schemaValue); err != nil {
		return nil, ErrDescriptorInvalid
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		return nil, ErrDescriptorInvalid
	}
	return &InputValidator{schema: schema}, nil
}

func (validator *InputValidator) Validate(arguments strictjson.Value) error {
	if validator == nil || validator.schema == nil {
		return ErrArgumentsInvalid
	}
	value, ok := validationValue(arguments)
	if !ok || validator.schema.Validate(value) != nil {
		return ErrArgumentsInvalid
	}
	return nil
}

func validationValue(value strictjson.Value) (any, bool) {
	switch value.Type {
	case strictjson.ValueNull:
		return nil, true
	case strictjson.ValueBoolean:
		return value.Boolean, true
	case strictjson.ValueString:
		return value.String, true
	case strictjson.ValueNumber:
		encoded, err := strictjson.EncodeCompact(value)
		if err != nil {
			return nil, false
		}
		return json.Number(encoded), true
	case strictjson.ValueObject:
		object := make(map[string]any, len(value.Object))
		for _, member := range value.Object {
			if _, duplicate := object[member.Name]; duplicate {
				return nil, false
			}
			memberValue, ok := validationValue(member.Value)
			if !ok {
				return nil, false
			}
			object[member.Name] = memberValue
		}
		return object, true
	case strictjson.ValueArray:
		array := make([]any, len(value.Array))
		for index, item := range value.Array {
			itemValue, ok := validationValue(item)
			if !ok {
				return nil, false
			}
			array[index] = itemValue
		}
		return array, true
	default:
		return nil, false
	}
}
