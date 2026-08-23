package strictjson

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type closedValue struct {
	Name string `json:"name"`
}

func TestDecodeRejectsInvalidUntrustedJSON(t *testing.T) {
	t.Parallel()

	tests := map[string][]byte{
		"invalid UTF-8":    {'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', '}'},
		"duplicate member": []byte(`{"name":"first","name":"second"}`),
		"excessive depth":  []byte(strings.Repeat("[", 5) + "0" + strings.Repeat("]", 5)),
		"unknown member":   []byte(`{"name":"value","other":true}`),
		"trailing value":   []byte(`{"name":"value"} {}`),
	}
	for name, input := range tests {
		name, input := name, input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var destination closedValue
			err := Decode(input, &destination, Options{MaxBytes: 1024, MaxDepth: 4, RejectUnknownMembers: true})
			require.Error(t, err)
		})
	}
}

func TestDecodeReaderAcceptsNAndRejectsNPlusOneBytes(t *testing.T) {
	t.Parallel()

	input := []byte(`{"name":"value"}`)
	var destination map[string]any
	require.NoError(t, DecodeReader(bytes.NewReader(input), &destination, Options{MaxBytes: int64(len(input)), MaxDepth: 4}))

	err := DecodeReader(bytes.NewReader(input), &destination, Options{MaxBytes: int64(len(input) - 1), MaxDepth: 4})
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestDecodeAllowsExtensionsWhenDestinationIsOpen(t *testing.T) {
	t.Parallel()

	var destination closedValue
	require.NoError(t, Decode([]byte(`{"name":"value","extension":true}`), &destination, Options{MaxBytes: 1024, MaxDepth: 4}))
	require.Equal(t, "value", destination.Name)
}

func TestCanonicalEqualUsesJSONValueSemantics(t *testing.T) {
	t.Parallel()

	options := Options{MaxBytes: 1024, MaxDepth: 8}
	equal, err := CanonicalEqual(
		[]byte(`{"transport":{"kind":"stdio","arguments":["a","b"]},"revision":1.0}`),
		[]byte(`{"revision":1e0,"transport":{"arguments":["a","b"],"kind":"stdio"}}`),
		options,
	)
	require.NoError(t, err)
	require.True(t, equal)

	equal, err = CanonicalEqual([]byte(`["a","b"]`), []byte(`["b","a"]`), options)
	require.NoError(t, err)
	require.False(t, equal, "array order is significant")

	_, err = CanonicalEqual([]byte(`{"a":1,"a":1}`), []byte(`{"a":1}`), options)
	require.Error(t, err)
}
