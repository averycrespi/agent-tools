package authorization

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"regexp/syntax"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type ConstraintType string

type ConstraintOperator string

type ConstraintDiagnostic struct {
	Field   string
	Message string
}

type constraintError struct {
	field   string
	message string
}

func (err constraintError) Error() string {
	return fmt.Sprintf("%s: constraint: %s", ErrInvalidInput, err.message)
}

func (constraintError) Unwrap() error { return ErrInvalidInput }

const (
	ConstraintNull    ConstraintType = "null"
	ConstraintBoolean ConstraintType = "boolean"
	ConstraintString  ConstraintType = "string"
	ConstraintNumber  ConstraintType = "number"

	ConstraintEquals ConstraintOperator = "equals"
	ConstraintRegex  ConstraintOperator = "regex"
)

type ConstraintAtom struct {
	Operator ConstraintOperator
	Pointer  string
	Type     ConstraintType
	Boolean  bool
	String   string
	Number   string
}

type CompiledConstraint struct {
	raw     []byte
	version int
	atoms   []constraintAtom
}

type constraintAtom struct {
	operator          ConstraintOperator
	pointer           string
	segments          []string
	expected          strictjson.Value
	expression        *regexp.Regexp
	regexInstructions int64
}

func CompileConstraint(contents []byte) (CompiledConstraint, error) {
	value, err := strictjson.ParseValue(contents, strictjson.Options{
		MaxBytes: mustLimit("constraint_bytes"),
		MaxDepth: int(mustLimit("json_depth")),
	})
	if err != nil {
		return CompiledConstraint{}, invalidConstraint("JSON is malformed")
	}
	if value.Type != strictjson.ValueObject {
		return CompiledConstraint{}, invalidConstraint("root must be an object")
	}

	version := 1
	var equals, expressions *strictjson.Value
	if len(value.Object) == 1 && value.Object[0].Name == "equals" {
		equals = &value.Object[0].Value
	} else {
		version = 2
		sawVersion := false
		for index := range value.Object {
			member := &value.Object[index]
			switch member.Name {
			case "version":
				if member.Value.Type != strictjson.ValueNumber || !jsonNumberEquals(member.Value.Number, 2) {
					return CompiledConstraint{}, invalidConstraint("version must be 2")
				}
				sawVersion = true
			case "equals":
				equals = &member.Value
			case "regex":
				expressions = &member.Value
			default:
				return CompiledConstraint{}, invalidConstraint("root contains an unknown member")
			}
		}
		if !sawVersion {
			return CompiledConstraint{}, invalidConstraint("version 2 is required")
		}
	}

	atoms := make([]constraintAtom, 0)
	if equals != nil {
		compiled, compileErr := compileEquals(*equals)
		if compileErr != nil {
			return CompiledConstraint{}, compileErr
		}
		atoms = append(atoms, compiled...)
	}
	if expressions != nil {
		compiled, compileErr := compileRegex(*expressions)
		if compileErr != nil {
			return CompiledConstraint{}, compileErr
		}
		atoms = append(atoms, compiled...)
	}
	if len(atoms) < 1 || int64(len(atoms)) > mustLimit("constraint_atoms") {
		return CompiledConstraint{}, invalidConstraint("total atom count is invalid")
	}
	return CompiledConstraint{raw: append([]byte(nil), contents...), version: version, atoms: atoms}, nil
}

func compileEquals(value strictjson.Value) ([]constraintAtom, error) {
	if value.Type != strictjson.ValueObject {
		return nil, invalidConstraintAt("/equals", "equals must be an object")
	}
	atoms := make([]constraintAtom, 0, len(value.Object))
	for _, member := range value.Object {
		field := constraintField("equals", member.Name)
		segments, err := compileJSONPointer(member.Name)
		if err != nil {
			return nil, invalidConstraintAt(field, "JSON pointer is invalid")
		}
		switch member.Value.Type {
		case strictjson.ValueNull, strictjson.ValueBoolean, strictjson.ValueString, strictjson.ValueNumber:
		default:
			return nil, invalidConstraintAt(field, "equality value must be scalar")
		}
		atoms = append(atoms, constraintAtom{operator: ConstraintEquals, pointer: member.Name, segments: segments, expected: member.Value})
	}
	return atoms, nil
}

func compileRegex(value strictjson.Value) ([]constraintAtom, error) {
	if value.Type != strictjson.ValueObject {
		return nil, invalidConstraintAt("/regex", "regex must be an object")
	}
	atoms := make([]constraintAtom, 0, len(value.Object))
	totalInstructions := int64(0)
	for _, member := range value.Object {
		field := constraintField("regex", member.Name)
		segments, err := compileJSONPointer(member.Name)
		if err != nil {
			return nil, invalidConstraintAt(field, "JSON pointer is invalid")
		}
		if member.Value.Type != strictjson.ValueString {
			return nil, invalidConstraintAt(field, "pattern must be a string")
		}
		pattern := member.Value.String
		if int64(len(pattern)) > mustLimit("constraint_regex_pattern_bytes") {
			return nil, invalidConstraintAt(field, "pattern exceeds the byte limit")
		}
		if _, parseErr := syntax.Parse(pattern, syntax.Perl); parseErr != nil {
			return nil, invalidConstraintAt(field, "pattern is not valid RE2")
		}
		anchored := `\A(?:` + pattern + `)\z`
		parsed, parseErr := syntax.Parse(anchored, syntax.Perl)
		if parseErr != nil {
			return nil, invalidConstraintAt(field, "pattern is not valid RE2")
		}
		program, compileErr := syntax.Compile(parsed.Simplify())
		if compileErr != nil || int64(len(program.Inst)) > mustLimit("constraint_regex_program_instructions") {
			return nil, invalidConstraintAt(field, "compiled pattern exceeds the size limit")
		}
		totalInstructions += int64(len(program.Inst))
		if totalInstructions > mustLimit("constraint_regex_total_program_instructions") {
			return nil, invalidConstraintAt("/regex", "compiled patterns exceed the total size limit")
		}
		expression, compileErr := regexp.Compile(anchored)
		if compileErr != nil {
			return nil, invalidConstraintAt(field, "pattern is not valid RE2")
		}
		atoms = append(atoms, constraintAtom{
			operator: ConstraintRegex, pointer: member.Name, segments: segments,
			expected: member.Value, expression: expression, regexInstructions: int64(len(program.Inst)),
		})
	}
	return atoms, nil
}

func (constraint CompiledConstraint) cacheWeight() int64 {
	weight := int64(len(constraint.raw) + len(constraint.atoms)*512)
	for _, atom := range constraint.atoms {
		weight += atom.regexInstructions * 64
	}
	return weight
}

func (constraint CompiledConstraint) JSON() []byte {
	return append([]byte(nil), constraint.raw...)
}

func (constraint CompiledConstraint) Version() int {
	return constraint.version
}

func (constraint CompiledConstraint) Atoms() []ConstraintAtom {
	atoms := make([]ConstraintAtom, len(constraint.atoms))
	for index, atom := range constraint.atoms {
		value := ConstraintAtom{Operator: atom.operator, Pointer: atom.pointer}
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

func ConstraintErrorDiagnostic(err error) (ConstraintDiagnostic, bool) {
	var target constraintError
	if !errors.As(err, &target) {
		return ConstraintDiagnostic{}, false
	}
	return ConstraintDiagnostic{Field: target.field, Message: target.message}, true
}

func constraintField(operator, pointer string) string {
	token := strings.ReplaceAll(strings.ReplaceAll(pointer, "~", "~0"), "/", "~1")
	return "/" + operator + "/" + token
}

func jsonNumberEquals(value string, expected int64) bool {
	parsed, ok := new(big.Rat).SetString(value)
	return ok && parsed.Cmp(big.NewRat(expected, 1)) == 0
}

func invalidConstraint(reason string) error {
	return invalidConstraintAt("", reason)
}

func invalidConstraintAt(field, reason string) error {
	return constraintError{field: field, message: reason}
}
