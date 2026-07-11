package scrub

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScrubCredentialValues(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		"github=ghp_abcdefghijklmnopqrstuvwxyz1234567890",
		"aws=AKIAIOSFODNN7EXAMPLE",
		"slack=" + "xo" + "xb-123456789012-123456789012-abcdefghijklmnopqrstuvwx",
		"openai=sk-abcdefghijklmnopqrstuvwxyz123456",
		"jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signaturevalue",
		"Authorization: Bearer secret-value-123",
		"password = hunter2",
		`{"password":"abc","aws_secret_access_key":"short"}`,
		"-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----",
	}, "\n")

	got := Scrub(input)
	for _, secret := range []string{"ghp_", "AKIAIOS", "xoxb-", "sk-abc", "eyJhbGci", "secret-value", "hunter2", `"abc"`, `"short"`, "\nsecret\n"} {
		require.NotContains(t, got, secret)
	}
	require.Equal(t, got, Scrub(got), "scrubbing must be idempotent")
}

func TestScrubPreservesDiagnosticsAndNearMisses(t *testing.T) {
	t.Parallel()

	input := "FAIL Error Trace: mcp_call failed MCP error fetch failed tokenization=ok monkey=banana"
	require.Equal(t, input, Scrub(input))
}
