package workflow

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type InputValues map[string]any

func (d *Definition) ValidateInputs(raw map[string]string) (InputValues, error) {
	for key := range raw {
		if _, exists := d.Inputs[key]; !exists {
			return nil, fmt.Errorf("unknown input %s", key)
		}
	}

	values := make(InputValues, len(d.Inputs))
	inputNames := make([]string, 0, len(d.Inputs))
	for name := range d.Inputs {
		inputNames = append(inputNames, name)
	}
	sort.Strings(inputNames)

	for _, name := range inputNames {
		schema := d.Inputs[name]
		rawValue, provided := raw[name]
		var value any
		switch {
		case provided:
			coerced, err := coerceInput(name, schema.Type, rawValue)
			if err != nil {
				return nil, err
			}
			value = coerced
		case schema.Default != nil:
			coerced, err := coerceDefault(name, schema.Type, schema.Default)
			if err != nil {
				return nil, err
			}
			value = coerced
		case schema.Required:
			return nil, fmt.Errorf("missing required input %s", name)
		default:
			continue
		}

		if len(schema.Enum) > 0 && !enumContains(schema.Enum, value) {
			return nil, fmt.Errorf("input %s must be one of %s", name, strings.Join(schema.Enum, ", "))
		}
		values[name] = value
	}
	return values, nil
}

func coerceInput(name string, inputType string, raw string) (any, error) {
	switch inputType {
	case InputString:
		return raw, nil
	case InputInteger:
		value, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("input %s must be an integer", name)
		}
		return value, nil
	case InputBoolean:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("input %s must be a boolean", name)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("input %s has unsupported type %s", name, inputType)
	}
}

func coerceDefault(name string, inputType string, value any) (any, error) {
	switch typed := value.(type) {
	case string:
		return coerceInput(name, inputType, typed)
	case int:
		if inputType != InputInteger {
			return nil, fmt.Errorf("input %s default does not match type %s", name, inputType)
		}
		return typed, nil
	case bool:
		if inputType != InputBoolean {
			return nil, fmt.Errorf("input %s default does not match type %s", name, inputType)
		}
		return typed, nil
	default:
		return nil, fmt.Errorf("input %s default has unsupported value", name)
	}
}

func enumContains(enum []string, value any) bool {
	stringValue := fmt.Sprint(value)
	for _, allowed := range enum {
		if stringValue == allowed {
			return true
		}
	}
	return false
}
