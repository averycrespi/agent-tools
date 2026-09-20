package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponseValidationDetails(t *testing.T) {
	const canary = "PRIVATE-VALUE-KEY-VALIDATOR-CANARY"
	for _, tc := range []struct{ name, tool, body, code, path, expected, observed string }{
		{"missing", "list_models", `{"models":[{"description":"` + canary + `","release_date":"2026-01-01"}]}`, "missing", "$.models.[].name", "", ""},
		{"type", "list_models", `{"models":[{"name":7,"description":"` + canary + `","release_date":"2026-01-01"}]}`, "type", "$.models.[].name", "string", "number"},
		{"date-pattern", "list_models", strings.Replace(models, "2026-09-15", canary, 1), "invalid_date", "$.models.[].release_date", "", ""},
		{"date-semantic", "list_models", strings.Replace(models, "2026-09-15", "2026-02-30", 1), "invalid_date", "$.models.[].release_date", "", ""},
		{"dynamic-key", "evaluate", `{"model":"x","answers":{"` + canary + `":{"type":"noul","noul":"` + canary + `"}},"usage":{"input_tokens":1,"output_tokens":2}}`, "type", "$.answers.*.noul", "number", "string"},
		{"correspondence", "evaluate", strings.Replace(result, `"route":`, `"`+canary+`":`, 1), "correspondence", "$.answers", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, attempts := fixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) })
			args := object{}
			if tc.tool == "evaluate" {
				args = decode(t, mixed)
			}
			_, err := client.Call(t.Context(), tc.tool, args)
			require.Error(t, err)
			d, ok := Diagnostic(err)
			require.True(t, ok)
			require.Equal(t, 2, d.Version)
			require.NotNil(t, d.Validation)
			var v ValidationViolation
			for _, candidate := range d.Validation.Violations {
				if candidate.Code == tc.code && candidate.Path == tc.path {
					v = candidate
					break
				}
			}
			require.Equal(t, tc.code, v.Code)
			require.Equal(t, tc.path, v.Path)
			require.Equal(t, tc.expected, v.Expected)
			require.Equal(t, tc.observed, v.Observed)
			require.False(t, d.Validation.Truncated)
			raw, err := json.Marshal(d)
			require.NoError(t, err)
			require.LessOrEqual(t, len(raw), validationDiagnosticMaxBytes)
			require.NotContains(t, string(raw), canary)
			require.EqualValues(t, 1, attempts.Load())
		})
	}
}

func TestMixedStructuralAndSemanticValidation(t *testing.T) {
	for _, tc := range []struct{ tool, body, semantic string }{
		{"list_models", `{"models":[{"name":7,"description":"fixture","release_date":"2026-02-30"}]}`, "invalid_date"},
		{"evaluate", strings.Replace(strings.Replace(result, `"route":`, `"different":`, 1), `"model":"jev-1.13.0"`, `"model":7`, 1), "correspondence"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			client, attempts := fixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) })
			args := object{}
			if tc.tool == "evaluate" {
				args = decode(t, mixed)
			}
			_, err := client.Call(t.Context(), tc.tool, args)
			d, ok := Diagnostic(err)
			require.True(t, ok)
			require.Len(t, d.Validation.Violations, 2)
			require.Equal(t, tc.semantic, d.Validation.Violations[0].Code)
			require.Equal(t, "type", d.Validation.Violations[1].Code)
			require.False(t, d.Validation.Truncated)
			require.EqualValues(t, 1, attempts.Load())
		})
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"answers":[]}`, `{"answers":{"route":null,"quality":7,"urgent":{}}}`, strings.Replace(result, `"probabilities":{"a":0.9,"b":0.1}`, `"probabilities":null`, 1), strings.Replace(result, `"choice":"a"`, `"choice":null`, 1), strings.Replace(result, `"score":0.7`, `"score":null`, 1)} {
		client, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		require.NotPanics(t, func() { _, err := client.Call(t.Context(), "evaluate", decode(t, mixed)); require.Error(t, err) })
	}
}

func TestResponseValidationSelection(t *testing.T) {
	bodies := []string{`{"models":[{},{"name":7,"description":7,"release_date":7}]}`, `{"models":[{"name":7,"description":7,"release_date":7},{},{}]}`}
	var previous []byte
	for _, body := range bodies {
		var value any
		require.NoError(t, json.Unmarshal([]byte(body), &value))
		d, ok := Diagnostic(validationFailure("models_result", responseViolations("models_result", value)))
		require.True(t, ok)
		require.GreaterOrEqual(t, len(d.Validation.Violations), 2)
		require.True(t, d.Validation.Truncated)
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		require.LessOrEqual(t, len(raw), validationDiagnosticMaxBytes)
		if previous != nil {
			require.Equal(t, string(previous), string(raw))
		}
		previous = raw
	}
}
