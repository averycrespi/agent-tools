package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerPrivateHTTPInputStrictBoolean(t *testing.T) {
	const prefix = `{"kind":"streamable_http","url":"http://notes-mcp:8080/mcp","protocol_mode":"auto","authentication":{"mode":"none"}`
	for _, extra := range []string{"", `,"allow_private_http":false`, `,"allow_private_http":true`} {
		require.NoError(t, validateServerTransportInput(json.RawMessage(prefix+extra+"}")))
	}
	for _, value := range []string{`null`, `"true"`, `1`, `[]`, `{}`} {
		require.Error(t, validateServerTransportInput(json.RawMessage(prefix+`,"allow_private_http":`+value+"}")))
	}
}
