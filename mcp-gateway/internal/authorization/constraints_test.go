package authorization

import (
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileConstraintPreservesPointersScalarsAndNumberTokens(t *testing.T) {
	contents := []byte(`{"equals":{"/string":"value","/boolean":true,"/null":null,"/one":1,"/decimal":1.0,"/exponent":1e0,"/a~1b/~0/x//0":false,"/prefix":"x","/prefix/child":"y"}}`)
	compiled, err := CompileConstraint(contents)
	require.NoError(t, err)
	require.Len(t, compiled.atoms, 9)
	assert.Equal(t, []string{"string"}, compiled.atoms[0].segments)
	assert.Equal(t, strictjson.ValueString, compiled.atoms[0].expected.Type)
	assert.Equal(t, "value", compiled.atoms[0].expected.String)
	assert.Equal(t, strictjson.ValueBoolean, compiled.atoms[1].expected.Type)
	assert.True(t, compiled.atoms[1].expected.Boolean)
	assert.Equal(t, strictjson.ValueNull, compiled.atoms[2].expected.Type)
	assert.Equal(t, []string{"1", "1.0", "1e0"}, []string{compiled.atoms[3].expected.Number, compiled.atoms[4].expected.Number, compiled.atoms[5].expected.Number})
	assert.Equal(t, []string{"a/b", "~", "x", "", "0"}, compiled.atoms[6].segments)
	assert.Equal(t, []string{"prefix"}, compiled.atoms[7].segments)
	assert.Equal(t, []string{"prefix", "child"}, compiled.atoms[8].segments, "prefix pairs are intentionally accepted")
	assert.Equal(t, string(contents), string(compiled.JSON()))
}

func TestCompileConstraintAcceptsV2EqualityAndRegexAtoms(t *testing.T) {
	contents := []byte(`{"version":2,"equals":{"/region":"us","/attempt":1.0},"regex":{"/resource":"[a-z]+/[0-9]+"}}`)
	compiled, err := CompileConstraint(contents)
	require.NoError(t, err)
	assert.Equal(t, 2, compiled.Version())
	assert.Equal(t, []ConstraintAtom{
		{Operator: ConstraintEquals, Pointer: "/region", Type: ConstraintString, String: "us"},
		{Operator: ConstraintEquals, Pointer: "/attempt", Type: ConstraintNumber, Number: "1.0"},
		{Operator: ConstraintRegex, Pointer: "/resource", Type: ConstraintString, String: "[a-z]+/[0-9]+"},
	}, compiled.Atoms())
	assert.Equal(t, string(contents), string(compiled.JSON()))
}

func TestCompileConstraintRejectsInvalidV2ShapesAndRegex(t *testing.T) {
	oversizedPattern := strings.Repeat("a", int(mustLimit("constraint_regex_pattern_bytes"))+1)
	oversizedProgram := strings.Repeat("a{1000}", 5)
	for _, input := range []string{
		`{"version":1,"equals":{"/x":1}}`,
		`{"version":3,"equals":{"/x":1}}`,
		`{"version":2}`,
		`{"version":2,"equals":{},"regex":{}}`,
		`{"version":2,"other":{"/x":1}}`,
		`{"version":2,"regex":{"/x":1}}`,
		`{"version":2,"regex":{"/x":"["}}`,
		`{"version":2,"regex":{"/x":"x)\\z|foo(?:"}}`,
		`{"version":2,"regex":{"/x":` + fmt.Sprintf("%q", oversizedPattern) + `}}`,
		`{"version":2,"regex":{"/x":` + fmt.Sprintf("%q", oversizedProgram) + `}}`,
	} {
		t.Run(input, func(t *testing.T) {
			_, err := CompileConstraint([]byte(input))
			assert.ErrorIs(t, err, ErrInvalidInput)
		})
	}
}

func TestCompileConstraintAcceptsEscapedMemberNames(t *testing.T) {
	compiled, err := CompileConstraint([]byte(`{"equ\u0061ls":{"/\u0078":1}}`))
	require.NoError(t, err)
	require.Len(t, compiled.atoms, 1)
	assert.Equal(t, "/x", compiled.atoms[0].pointer)
	assert.Equal(t, []string{"x"}, compiled.atoms[0].segments)

	_, err = CompileConstraint([]byte(`{"equals":{"/x":1,"/\u0078":2}}`))
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestCompileConstraintAcceptsExactBoundsAndRejectsNPlusOne(t *testing.T) {
	atoms := make([]string, 16)
	for index := range atoms {
		atoms[index] = fmt.Sprintf(`"/%d":%d`, index, index)
	}
	compiled, err := CompileConstraint([]byte(`{"equals":{` + strings.Join(atoms, ",") + `}}`))
	require.NoError(t, err)
	assert.Len(t, compiled.atoms, 16)

	atoms = append(atoms, `"/16":16`)
	_, err = CompileConstraint([]byte(`{"equals":{` + strings.Join(atoms, ",") + `}}`))
	assert.ErrorIs(t, err, ErrInvalidInput)

	pointerAtLimit := "/" + strings.Repeat("p", 255)
	_, err = CompileConstraint([]byte(`{"equals":{"` + pointerAtLimit + `":null}}`))
	require.NoError(t, err)
	_, err = CompileConstraint([]byte(`{"equals":{"` + pointerAtLimit + `p":null}}`))
	assert.ErrorIs(t, err, ErrInvalidInput)

	prefix := `{"equals":{"/x":"`
	suffix := `"}}`
	exact := []byte(prefix + strings.Repeat("v", 8192-len(prefix)-len(suffix)) + suffix)
	require.Len(t, exact, 8192)
	_, err = CompileConstraint(exact)
	require.NoError(t, err)
	_, err = CompileConstraint(append(exact[:len(exact)-len(suffix)], append([]byte("v"), []byte(suffix)...)...))
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestCompileConstraintRejectsInvalidClosedShapesPointersAndContainers(t *testing.T) {
	tests := []string{
		`null`, `{}`, `{"other":{}}`, `{"equals":{},"other":{}}`,
		`{"equals":{}}`, `{"equals":null}`, `{"equals":[]}`,
		`{"equals":{"":null}}`, `{"equals":{"relative":null}}`,
		`{"equals":{"/~":null}}`, `{"equals":{"/~2":null}}`,
		`{"equals":{"/x":{}}}`, `{"equals":{"/x":[]}}`,
		`{"equals":{"/x":1,"/x":2}}`, `{"equals":{"\u0000":null}}`,
		`{"equals":{"/x":NaN}}`, `{"equals":{"/x":1}} trailing`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := CompileConstraint([]byte(input))
			assert.ErrorIs(t, err, ErrInvalidInput)
		})
	}
}

func TestCompiledConstraintOwnsImmutableBytesAndTraversalMetadata(t *testing.T) {
	input := []byte(`{"equals":{"/a~1b":1.0}}`)
	compiled, err := CompileConstraint(input)
	require.NoError(t, err)
	input[0] = '['
	returned := compiled.JSON()
	returned[0] = '['
	compiled.atoms[0].segments[0] = "mutated"

	assert.JSONEq(t, `{"equals":{"/a~1b":1.0}}`, string(compiled.JSON()))
	second, err := CompileConstraint(compiled.JSON())
	require.NoError(t, err)
	assert.Equal(t, []string{"a/b"}, second.atoms[0].segments)
	assert.Equal(t, "1.0", second.atoms[0].expected.Number)
}

func TestCompileConstraintGeneratedScalarAndPointerCorpus(t *testing.T) {
	scalars := []string{`null`, `true`, `false`, `""`, `"text"`, `0`, `-0`, `1.0`, `1e+2`, `1E-2`}
	pointers := []string{`/`, `/0`, `//`, `/a/b`, `/a~0b`, `/a~1b`, `/雪`}
	for _, scalar := range scalars {
		for _, pointer := range pointers {
			input := []byte(`{"equals":{"` + pointer + `":` + scalar + `}}`)
			compiled, err := CompileConstraint(input)
			require.NoError(t, err, "%s", input)
			require.Len(t, compiled.atoms, 1)
			assert.NotEmpty(t, compiled.atoms[0].segments)
		}
	}
}

func FuzzCompileConstraint(f *testing.F) {
	for _, seed := range []string{
		`{"equals":{"/x":1}}`, `{"equals":{"/a~1b":1.0,"/":null}}`,
		`{"equals":{}}`, `{"equals":{"/~2":true}}`, `[]`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		compiled, err := CompileConstraint([]byte(input))
		if err != nil {
			return
		}
		require.NotEmpty(t, compiled.atoms)
		require.LessOrEqual(t, len(compiled.atoms), 16)
		require.LessOrEqual(t, len(compiled.JSON()), 8192)
		for _, atom := range compiled.atoms {
			require.NotEmpty(t, atom.pointer)
			require.LessOrEqual(t, len(atom.pointer), 256)
			require.NotEmpty(t, atom.segments)
			require.Contains(t, []strictjson.ValueType{strictjson.ValueNull, strictjson.ValueBoolean, strictjson.ValueString, strictjson.ValueNumber}, atom.expected.Type)
		}
	})
}
