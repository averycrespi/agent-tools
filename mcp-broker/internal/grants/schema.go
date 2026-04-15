package grants

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schema is a compiled JSON Schema that can validate a decoded tool args map.
type Schema struct {
	compiled *jsonschema.Schema
}

// CompileSchema parses and compiles the given JSON Schema fragment.
func CompileSchema(raw json.RawMessage) (*Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("unmarshalling schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	const url = "grant://schema.json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("adding schema resource: %w", err)
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}
	return &Schema{compiled: s}, nil
}

// Validate reports whether args satisfies the schema.
func (s *Schema) Validate(args map[string]any) error {
	// jsonschema/v6 validates arbitrary any; convert nil to empty object
	// so "required" keywords fail cleanly on empty input.
	var v any = args
	if args == nil {
		v = map[string]any{}
	}
	return s.compiled.Validate(v)
}
