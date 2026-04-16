package grants

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAgainstInputSchema_UnknownArg(t *testing.T) {
	toolSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"branch": {"type": "string"}, "force": {"type": "boolean"}}
	}`)
	argSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"branc": {"const": "feat/foo"}},
		"required": ["branc"]
	}`)
	err := ValidateAgainstInputSchema(argSchema, toolSchema)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown arg")
	require.Contains(t, err.Error(), `did you mean "branch"`)
}

func TestValidateAgainstInputSchema_WrongType(t *testing.T) {
	toolSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"force": {"type": "boolean"}}
	}`)
	argSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"force": {"const": "feat/foo"}}
	}`)
	err := ValidateAgainstInputSchema(argSchema, toolSchema)
	require.Error(t, err)
	require.Contains(t, err.Error(), "type mismatch")
}

func TestValidateAgainstInputSchema_OK(t *testing.T) {
	toolSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"branch": {"type": "string"}, "force": {"type": "boolean"}}
	}`)
	argSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"branch": {"const": "feat/foo"}, "force": {"const": false}}
	}`)
	require.NoError(t, ValidateAgainstInputSchema(argSchema, toolSchema))
}
