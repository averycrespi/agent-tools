package strictjson

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseValueRetainsTypesTokensAndObjectOrder(t *testing.T) {
	t.Parallel()

	value, err := ParseValue([]byte(`{"z":null,"\u0061":true,"string":"value","numbers":[1,1.0,1e0,1e10000],"object":{"k":false}}`), Options{MaxBytes: 1024, MaxDepth: 8})
	require.NoError(t, err)
	require.Equal(t, ValueObject, value.Type)
	require.Equal(t, []string{"z", "a", "string", "numbers", "object"}, memberNames(value.Object))

	require.Equal(t, ValueNull, value.Object[0].Value.Type)
	require.Equal(t, ValueBoolean, value.Object[1].Value.Type)
	require.True(t, value.Object[1].Value.Boolean)
	require.Equal(t, ValueString, value.Object[2].Value.Type)
	require.Equal(t, "value", value.Object[2].Value.String)

	numbers := value.Object[3].Value
	require.Equal(t, ValueArray, numbers.Type)
	require.Equal(t, []string{"1", "1.0", "1e0", "1e10000"}, []string{numbers.Array[0].Number, numbers.Array[1].Number, numbers.Array[2].Number, numbers.Array[3].Number})
	for _, number := range numbers.Array {
		require.Equal(t, ValueNumber, number.Type)
	}

	nested := value.Object[4].Value
	require.Equal(t, ValueObject, nested.Type)
	require.Equal(t, "k", nested.Object[0].Name)
	require.Equal(t, ValueBoolean, nested.Object[0].Value.Type)
	require.False(t, nested.Object[0].Value.Boolean)
}

func TestParseValueGeneratedObjectOrderCorpus(t *testing.T) {
	t.Parallel()

	for _, order := range permutations([]string{"alpha", "beta", "gamma"}) {
		var input strings.Builder
		input.WriteByte('{')
		for index, name := range order {
			if index > 0 {
				input.WriteByte(',')
			}
			fmt.Fprintf(&input, `%q:%d`, name, index)
		}
		input.WriteByte('}')

		value, err := ParseValue([]byte(input.String()), Options{MaxBytes: 1024, MaxDepth: 4})
		require.NoError(t, err)
		require.Equal(t, order, memberNames(value.Object))
		for index, member := range value.Object {
			require.Equal(t, ValueNumber, member.Value.Type)
			require.Equal(t, fmt.Sprint(index), member.Value.Number)
		}
	}
}

func TestParseValueReusesStrictJSONProtections(t *testing.T) {
	t.Parallel()

	invalidUTF8 := []byte{'[', '"', 0xff, '"', ']'}
	tests := map[string][]byte{
		"invalid UTF-8":          invalidUTF8,
		"duplicate escaped name": []byte(`{"a":1,"\u0061":2}`),
		"trailing value":         []byte(`{} []`),
		"malformed leading zero": []byte(`[01]`),
		"malformed decimal":      []byte(`[1.]`),
		"malformed exponent":     []byte(`[1e]`),
	}
	for name, input := range tests {
		name, input := name, input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseValue(input, Options{MaxBytes: 1024, MaxDepth: 8})
			require.Error(t, err)
		})
	}

	_, err := ParseValue([]byte(`null`), Options{MaxBytes: 0, MaxDepth: 2})
	require.ErrorContains(t, err, "byte limit")
	_, err = ParseValue([]byte(`null`), Options{MaxBytes: 1024, MaxDepth: 0})
	require.ErrorContains(t, err, "depth limit")

	_, err = ParseValue([]byte(`[[[0]]]`), Options{MaxBytes: 1024, MaxDepth: 2})
	require.ErrorContains(t, err, "depth")
	value, err := ParseValue([]byte(`[[0]]`), Options{MaxBytes: 1024, MaxDepth: 2})
	require.NoError(t, err)
	require.Equal(t, ValueArray, value.Type)

	input := []byte(`{"number":1.0}`)
	_, err = ParseValue(input, Options{MaxBytes: int64(len(input) - 1), MaxDepth: 8})
	require.ErrorIs(t, err, ErrTooLarge)
	value, err = ParseValue(input, Options{MaxBytes: int64(len(input)), MaxDepth: 8})
	require.NoError(t, err)
	require.Equal(t, "1.0", value.Object[0].Value.Number)
}

func TestParseValueReaderAcceptsNAndRejectsNPlusOne(t *testing.T) {
	t.Parallel()

	input := []byte(`[1e0]`)
	value, err := ParseValueReader(bytes.NewReader(input), Options{MaxBytes: int64(len(input)), MaxDepth: 2})
	require.NoError(t, err)
	require.Equal(t, "1e0", value.Array[0].Number)

	_, err = ParseValueReader(bytes.NewReader(input), Options{MaxBytes: int64(len(input) - 1), MaxDepth: 2})
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestParseValueLeavesCanonicalEqualityUnchanged(t *testing.T) {
	t.Parallel()

	options := Options{MaxBytes: 1024, MaxDepth: 8}
	left, err := ParseValue([]byte(`{"n":1.0}`), options)
	require.NoError(t, err)
	right, err := ParseValue([]byte(`{"n":1e0}`), options)
	require.NoError(t, err)
	require.NotEqual(t, left.Object[0].Value.Number, right.Object[0].Value.Number)

	equal, err := CanonicalEqual([]byte(`{"n":1.0}`), []byte(`{"n":1e0}`), options)
	require.NoError(t, err)
	require.True(t, equal, "S2 canonical equality intentionally normalizes equivalent number spellings")
}

func FuzzParseValueRetainsNumberTokens(f *testing.F) {
	for _, seed := range []string{"0", "-0", "1", "1.0", "1e0", "1E+2", "-12.3400e-5", "1e10000"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		trimmed := strings.TrimSpace(input)
		value, err := ParseValue([]byte(input), Options{MaxBytes: 1024, MaxDepth: 8})
		if err != nil || value.Type != ValueNumber {
			return
		}
		require.Equal(t, trimmed, value.Number)
	})
}

func permutations(values []string) [][]string {
	result := make([][]string, 0)
	var generate func(int)
	generate = func(index int) {
		if index == len(values) {
			result = append(result, append([]string(nil), values...))
			return
		}
		for candidate := index; candidate < len(values); candidate++ {
			values[index], values[candidate] = values[candidate], values[index]
			generate(index + 1)
			values[index], values[candidate] = values[candidate], values[index]
		}
	}
	generate(0)
	return result
}

func memberNames(members []Member) []string {
	names := make([]string, len(members))
	for index, member := range members {
		names[index] = member.Name
	}
	return names
}
