// Package provider implements the bounded, one-shot TypeSafe inference contract.
package provider

import (
	"encoding/json"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	MaxRequestBytes  = 256 << 10
	MaxResponseBytes = 512 << 10
	MaxDepth         = 32
	MaxQuestions     = 32
	MaxConcurrent    = 4
)

type object = map[string]any

func closed(properties object, required ...string) object {
	return object{"type": "object", "properties": properties, "required": append([]string{}, required...), "additionalProperties": false}
}
func entry(nullable bool) object {
	types := []string{"string", "object", "array"}
	if nullable {
		types = append(types, "null")
	}
	return object{"type": types}
}
func dictionary(value object, minimum int) object {
	return object{"type": "object", "additionalProperties": value, "minProperties": minimum}
}
func array(value object, minimum int) object {
	return object{"type": "array", "items": value, "minItems": minimum}
}
func text() object                         { return object{"type": "string", "minLength": 1} }
func literal(value string) object          { return object{"const": value} }
func probability() object                  { return object{"type": "number", "minimum": 0, "maximum": 1} }
func encoded(value object) json.RawMessage { data, _ := json.Marshal(value); return data }

var EvaluateSchema = func() json.RawMessage {
	choice := closed(object{"type": literal("choice"), "instructions": entry(false), "criteria": dictionary(entry(true), 1)}, "type", "instructions", "criteria")
	score := closed(object{"type": literal("score"), "instructions": entry(false), "criteria": array(entry(true), 2)}, "type", "instructions", "criteria")
	noul := closed(object{"type": literal("noul"), "instructions": entry(false), "criteria": closed(object{"true": entry(true), "false": entry(true)})}, "type", "instructions")
	questions := dictionary(object{"oneOf": []any{choice, score, noul}}, 1)
	questions["maxProperties"] = MaxQuestions
	model := text()
	model["maxLength"] = 256
	model["pattern"] = `\S`
	return encoded(closed(object{"state": entry(false), "questions": questions, "model": model}, "state", "questions"))
}()

var ModelsSchema = encoded(closed(object{}))
var EvaluateOutputSchema = func() json.RawMessage {
	choice := closed(object{"type": literal("choice"), "choice": object{"type": "string"}, "confidence": probability(), "probabilities": dictionary(probability(), 1)}, "type", "choice", "probabilities")
	score := closed(object{"type": literal("score"), "score": object{"type": "number", "minimum": 0}, "confidence": probability(), "legend": dictionary(entry(true), 2), "probabilities": dictionary(probability(), 2)}, "type", "score", "legend", "probabilities")
	noul := closed(object{"type": literal("noul"), "noul": probability()}, "type", "noul")
	usage := closed(object{"input_tokens": object{"type": "integer", "minimum": 0}, "output_tokens": object{"type": "integer", "minimum": 0}}, "input_tokens", "output_tokens")
	return encoded(closed(object{"model": text(), "answers": dictionary(object{"oneOf": []any{choice, score, noul}}, 1), "usage": usage}, "model", "answers", "usage"))
}()
var ModelsOutputSchema = func() json.RawMessage {
	card := closed(object{"name": text(), "description": object{"type": "string"}, "release_date": object{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}}, "name", "description", "release_date")
	cards := array(card, 0)
	cards["maxItems"] = 256
	return encoded(closed(object{"models": cards}, "models"))
}()

var validators = sync.OnceValue(func() map[string]*jsonschema.Schema {
	result := make(map[string]*jsonschema.Schema)
	for name, raw := range map[string]json.RawMessage{"evaluate": EvaluateSchema, "list_models": ModelsSchema, "evaluate_result": EvaluateOutputSchema, "models_result": ModelsOutputSchema} {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			panic("invalid compiled-in schema")
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("schema.json", value); err != nil {
			panic("invalid compiled-in schema")
		}
		schema, err := compiler.Compile("schema.json")
		if err != nil {
			panic("invalid compiled-in schema")
		}
		result[name] = schema
	}
	return result
})

func valid(name string, value any) bool { return validators()[name].Validate(value) == nil }
