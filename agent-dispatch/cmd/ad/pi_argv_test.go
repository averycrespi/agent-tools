package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodePiArgvContainsNoNULAndRoundTrips(t *testing.T) {
	argv := []string{"pi", "--mode", "rpc", "--system-prompt", "hello world"}

	encoded, err := encodePiArgv(argv)
	require.NoError(t, err)
	require.NotContains(t, encoded, "\x00")

	decoded, err := decodePiArgv(encoded)
	require.NoError(t, err)
	require.Equal(t, argv, decoded)
}

func TestEncodePiArgvRejectsUnrepresentableNUL(t *testing.T) {
	_, err := encodePiArgv([]string{"bad" + string(rune(0))})
	require.Error(t, err)
	require.Contains(t, err.Error(), "NUL")
}

func TestLegacySplitNULIsNotUsedForEncodedPiArgv(t *testing.T) {
	encoded, err := encodePiArgv([]string{"pi", "--mode", "rpc"})
	require.NoError(t, err)
	require.False(t, strings.Contains(encoded, "\x00"))
}
