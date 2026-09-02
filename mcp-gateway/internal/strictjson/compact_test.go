package strictjson

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeCompactPreservesTokensOrderAndTypes(t *testing.T) {
	t.Parallel()

	input := []byte(" { \"z\" : [ 1.0 , 1e0 , true , null ], \"a\" : { \"escaped\" : \"<value>\" } } ")
	value, err := ParseValue(input, Options{MaxBytes: 1024, MaxDepth: 8})
	require.NoError(t, err)

	encoded, err := EncodeCompact(value)
	require.NoError(t, err)
	require.Equal(t, `{"z":[1.0,1e0,true,null],"a":{"escaped":"\u003cvalue\u003e"}}`, string(encoded))
	require.Equal(t, []string{"z", "a"}, memberNames(value.Object))
	require.Equal(t, []string{"1.0", "1e0"}, []string{value.Object[0].Value.Array[0].Number, value.Object[0].Value.Array[1].Number})
}

func TestEncodeCompactGeneratedObjectOrderCorpus(t *testing.T) {
	t.Parallel()

	for _, order := range permutations([]string{"alpha", "beta", "gamma"}) {
		value := Value{Type: ValueObject, Object: make([]Member, len(order))}
		var expected strings.Builder
		expected.WriteByte('{')
		for index, name := range order {
			value.Object[index] = Member{Name: name, Value: Value{Type: ValueNumber, Number: fmt.Sprint(index)}}
			if index > 0 {
				expected.WriteByte(',')
			}
			fmt.Fprintf(&expected, `%q:%d`, name, index)
		}
		expected.WriteByte('}')
		encoded, err := EncodeCompact(value)
		require.NoError(t, err)
		require.Equal(t, expected.String(), string(encoded))
	}
}

func TestEncodeCompactRejectsMalformedConstructedValues(t *testing.T) {
	t.Parallel()

	for _, value := range []Value{
		{},
		{Type: ValueNumber, Number: "true"},
		{Type: ValueNumber, Number: "01"},
		{Type: ValueType("unknown")},
	} {
		_, err := EncodeCompact(value)
		require.Error(t, err)
	}
}

func FuzzEncodeCompactRetainsParsedNumberToken(f *testing.F) {
	for _, seed := range []string{"0", "-0", "1.0", "1e0", "1E+2", "-12.3400e-5", "1e10000"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, number string) {
		value, err := ParseValue([]byte(number), Options{MaxBytes: 1024, MaxDepth: 4})
		if err != nil || value.Type != ValueNumber {
			return
		}
		encoded, err := EncodeCompact(value)
		if err != nil || string(encoded) != value.Number {
			t.Fatal("compact number token invariant failed")
		}
	})
}
