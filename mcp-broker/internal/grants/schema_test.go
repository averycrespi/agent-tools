package grants

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompileAndValidate(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"branch": {"const": "feat/foo"},
			"force":  {"const": false}
		},
		"required": ["branch", "force"]
	}`)

	s, err := CompileSchema(raw)
	require.NoError(t, err)

	require.NoError(t, s.Validate(map[string]any{
		"branch": "feat/foo",
		"force":  false,
	}))

	require.Error(t, s.Validate(map[string]any{
		"branch": "main",
		"force":  false,
	}), "wrong branch value must not match")

	require.Error(t, s.Validate(map[string]any{
		"branch": "feat/foo",
	}), "missing required field must not match")
}

func TestCompileRejectsMalformed(t *testing.T) {
	_, err := CompileSchema(json.RawMessage(`{"type": 123}`))
	require.Error(t, err)
}
