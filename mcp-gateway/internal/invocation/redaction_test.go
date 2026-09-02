package invocation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/require"
)

func TestRedactArgumentsUsesExactNormalizedKeySet(t *testing.T) {
	t.Parallel()

	secretKeys := []string{
		"authorization", "cookie", "password", "passwd", "secret", "token", "access-token", "refresh_token",
		"api_key", "private-key", "client_secret", "AUTH_OR-IZATION",
	}
	for _, key := range secretKeys {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			input := mustArguments(t, map[string]any{key: map[string]any{"nested": "value"}})
			before := cloneValue(input)
			capture, err := RedactArguments(input)
			require.NoError(t, err)
			require.JSONEq(t, `{"`+key+`":"[REDACTED]"}`, string(capture))
			require.Equal(t, before, input, "redaction must not mutate the arguments used by authorization and downstream")
		})
	}

	nearMisses := []string{"tokens", "access token", "api.key", "private/key", "clientsecrets", "töken", "_"}
	for _, key := range nearMisses {
		key := key
		t.Run("near-miss-"+key, func(t *testing.T) {
			t.Parallel()
			input := mustArguments(t, map[string]any{key: "visible"})
			capture, err := RedactArguments(input)
			require.NoError(t, err)
			require.JSONEq(t, `{"`+key+`":"visible"}`, string(capture))
		})
	}
}

func TestRedactArgumentsTraversesObjectsInsideArraysAndPreservesLexicalValues(t *testing.T) {
	t.Parallel()

	input := parseArguments(t, `{"z":1.0,"items":[{"Token":"hidden","keep":1e0},[true,{"client-secret":"hidden"}]],"a":"last"}`)
	before := cloneValue(input)
	capture, err := RedactArguments(input)
	require.NoError(t, err)
	require.Equal(t, `{"z":1.0,"items":[{"Token":"[REDACTED]","keep":1e0},[true,{"client-secret":"[REDACTED]"}]],"a":"last"}`, string(capture))
	require.Equal(t, before, input)
}

func TestRedactArgumentsAccepts8192BytesAndTruncates8193(t *testing.T) {
	t.Parallel()

	atLimit := strictjson.Value{Type: strictjson.ValueObject, Object: []strictjson.Member{{
		Name: "x", Value: strictjson.Value{Type: strictjson.ValueString, String: strings.Repeat("a", 8184)},
	}}}
	capture, err := RedactArguments(atLimit)
	require.NoError(t, err)
	require.Len(t, capture, 8192)
	require.NotEqual(t, `"[TRUNCATED]"`, string(capture))

	overLimit := strictjson.Value{Type: strictjson.ValueObject, Object: []strictjson.Member{{
		Name: "x", Value: strictjson.Value{Type: strictjson.ValueString, String: strings.Repeat("a", 8185)},
	}}}
	capture, err = RedactArguments(overLimit)
	require.NoError(t, err)
	require.Equal(t, `"[TRUNCATED]"`, string(capture))
}

func TestRedactArgumentsRequiresObjectAndValidValue(t *testing.T) {
	t.Parallel()

	_, err := RedactArguments(strictjson.Value{Type: strictjson.ValueArray})
	require.Error(t, err)
	_, err = RedactArguments(strictjson.Value{Type: strictjson.ValueObject, Object: []strictjson.Member{{
		Name: "x", Value: strictjson.Value{Type: strictjson.ValueNumber, Number: "invalid"},
	}}})
	require.Error(t, err)
}

func FuzzRedactArgumentsMatchesExactKeyPolicy(f *testing.F) {
	for _, seed := range []struct{ key, value string }{
		{"token", "canary"}, {"Access_Token", "canary"}, {"töken", "visible"}, {"ordinary", "visible"},
	} {
		f.Add(seed.key, seed.value)
	}
	f.Fuzz(func(t *testing.T, key, value string) {
		if len(key) > 512 || len(value) > 512 {
			return
		}
		input := mustArguments(t, map[string]any{key: value})
		capture, err := RedactArguments(input)
		if err != nil {
			t.Fatal("redaction unexpectedly failed")
		}
		redacted, err := strictjson.ParseValue(capture, strictjson.Options{MaxBytes: 8192, MaxDepth: 8})
		if err != nil || redacted.Type != strictjson.ValueObject || len(redacted.Object) != 1 {
			t.Fatal("redaction produced an invalid capture")
		}
		wantRedacted := isRedactedKey(input.Object[0].Name)
		gotRedacted := redacted.Object[0].Value.Type == strictjson.ValueString && redacted.Object[0].Value.String == "[REDACTED]"
		if wantRedacted != gotRedacted && value != "[REDACTED]" {
			t.Fatal("redaction key policy mismatch")
		}
	})
}

func parseArguments(t *testing.T, input string) strictjson.Value {
	t.Helper()
	value, err := strictjson.ParseValue([]byte(input), strictjson.Options{MaxBytes: 4 * 1024 * 1024, MaxDepth: 64})
	require.NoError(t, err)
	return value
}

func mustArguments(t *testing.T, value map[string]any) strictjson.Value {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return parseArguments(t, string(encoded))
}

func cloneValue(value strictjson.Value) strictjson.Value {
	clone := value
	clone.Object = append([]strictjson.Member(nil), value.Object...)
	for index := range clone.Object {
		clone.Object[index].Value = cloneValue(clone.Object[index].Value)
	}
	clone.Array = append([]strictjson.Value(nil), value.Array...)
	for index := range clone.Array {
		clone.Array[index] = cloneValue(clone.Array[index])
	}
	return clone
}
