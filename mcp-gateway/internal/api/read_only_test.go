package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadOnlyAPICompatibilityAndStrictBoolean(t *testing.T) {
	for _, value := range []string{"", "true", "false", "null", `"true"`, "1", "{}", "[]"} {
		t.Run(value, func(t *testing.T) {
			raw := rawGrantCreate{Description: json.RawMessage(`null`), PrincipalID: json.RawMessage(`"` + testID + `"`), Effect: json.RawMessage(`"allow"`), ServerID: json.RawMessage(`"` + testServerID + `"`), UpstreamName: json.RawMessage(`null`), Constraint: json.RawMessage(`null`), ExpiresAt: json.RawMessage(`null`)}
			suffix := ""
			if value != "" {
				raw.ReadOnly = json.RawMessage(value)
				suffix = `,"read_only":` + value
			}
			want := value == "" || value == "true" || value == "false"
			grant, ok := decodeGrantCreate(httptest.NewRecorder(), raw)
			require.Equal(t, want, ok)
			if ok {
				require.Equal(t, value == "true", grant.ReadOnly)
			}
			policy, ok := decodeApprovalPolicy(json.RawMessage(`{"scope":"server","target":"sample","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true` + suffix + `}`))
			require.Equal(t, want, ok)
			if ok {
				require.Equal(t, value == "true", policy.ReadOnly)
			}
		})
	}
}
