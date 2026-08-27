package authorization

import (
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type ConstraintType string

const (
	ConstraintNull    ConstraintType = "null"
	ConstraintBoolean ConstraintType = "boolean"
	ConstraintString  ConstraintType = "string"
	ConstraintNumber  ConstraintType = "number"
)

type ConstraintAtom struct {
	Pointer string
	Type    ConstraintType
	Boolean bool
	String  string
	Number  string
}

type CompiledConstraint struct {
	raw   []byte
	atoms []constraintAtom
}

type constraintAtom struct {
	pointer  string
	segments []string
	expected strictjson.Value
}

func CompileConstraint(contents []byte) (CompiledConstraint, error) {
	value, err := strictjson.ParseValue(contents, strictjson.Options{
		MaxBytes: mustLimit("constraint_bytes"),
		MaxDepth: int(mustLimit("json_depth")),
	})
	if err != nil {
		return CompiledConstraint{}, invalidConstraint("JSON is malformed")
	}
	if value.Type != strictjson.ValueObject || len(value.Object) != 1 || value.Object[0].Name != "equals" {
		return CompiledConstraint{}, invalidConstraint("root must contain only equals")
	}
	equals := value.Object[0].Value
	if equals.Type != strictjson.ValueObject || len(equals.Object) < 1 || int64(len(equals.Object)) > mustLimit("constraint_atoms") {
		return CompiledConstraint{}, invalidConstraint("equals atom count is invalid")
	}

	atoms := make([]constraintAtom, 0, len(equals.Object))
	for _, member := range equals.Object {
		segments, err := compileJSONPointer(member.Name)
		if err != nil {
			return CompiledConstraint{}, err
		}
		switch member.Value.Type {
		case strictjson.ValueNull, strictjson.ValueBoolean, strictjson.ValueString, strictjson.ValueNumber:
		default:
			return CompiledConstraint{}, invalidConstraint("equals values must be scalar")
		}
		atoms = append(atoms, constraintAtom{
			pointer:  member.Name,
			segments: append([]string(nil), segments...),
			expected: member.Value,
		})
	}
	return CompiledConstraint{raw: append([]byte(nil), contents...), atoms: atoms}, nil
}

func (constraint CompiledConstraint) JSON() []byte {
	return append([]byte(nil), constraint.raw...)
}

func (constraint CompiledConstraint) Atoms() []ConstraintAtom {
	atoms := make([]ConstraintAtom, len(constraint.atoms))
	for index, atom := range constraint.atoms {
		value := ConstraintAtom{Pointer: atom.pointer}
		switch atom.expected.Type {
		case strictjson.ValueNull:
			value.Type = ConstraintNull
		case strictjson.ValueBoolean:
			value.Type, value.Boolean = ConstraintBoolean, atom.expected.Boolean
		case strictjson.ValueString:
			value.Type, value.String = ConstraintString, atom.expected.String
		case strictjson.ValueNumber:
			value.Type, value.Number = ConstraintNumber, atom.expected.Number
		}
		atoms[index] = value
	}
	return atoms
}

func compileJSONPointer(pointer string) ([]string, error) {
	if pointer == "" || pointer[0] != '/' || int64(len(pointer)) > mustLimit("constraint_pointer_bytes") {
		return nil, invalidConstraint("JSON pointer is invalid")
	}
	tokens := strings.Split(pointer[1:], "/")
	segments := make([]string, len(tokens))
	for index, token := range tokens {
		var decoded strings.Builder
		decoded.Grow(len(token))
		for offset := 0; offset < len(token); offset++ {
			if token[offset] != '~' {
				decoded.WriteByte(token[offset])
				continue
			}
			if offset+1 >= len(token) {
				return nil, invalidConstraint("JSON pointer escape is invalid")
			}
			offset++
			switch token[offset] {
			case '0':
				decoded.WriteByte('~')
			case '1':
				decoded.WriteByte('/')
			default:
				return nil, invalidConstraint("JSON pointer escape is invalid")
			}
		}
		segments[index] = decoded.String()
	}
	return segments, nil
}

func invalidConstraint(reason string) error {
	return fmt.Errorf("%w: constraint: %s", ErrInvalidInput, reason)
}
