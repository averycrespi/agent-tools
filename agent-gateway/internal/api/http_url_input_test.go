package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPURLInputPreservesUnicodeScalars(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`"https://example.com/é"`, "https://example.com/é"},
		{`"https://example.com/\uD83D\uDE00/%2f"`, "https://example.com/😀/%2f"},
		{`"https://example.com/\ufffd"`, "https://example.com/�"},
		{`"https://example.com/\\ud800"`, `https://example.com/\ud800`},
	} {
		var got string
		require.True(t, decodeHTTPURLInput(json.RawMessage(tc.raw), &got))
		require.Equal(t, tc.want, got)
	}
	for _, raw := range []string{`null`, `1`, `"https://example.com/\ud800"`, `"https://example.com/\udfff"`, `"https://example.com/\ud800x"`, `"https://example.com/\ud800\u0041"`, `"https://example.com/\ud800\ud800"`} {
		var got string
		require.False(t, decodeHTTPURLInput(json.RawMessage(raw), &got))
	}
}
