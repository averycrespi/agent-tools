package downstream

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectOAuthChallengeRetainsOnlyBoundedReviewedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		status int
		values []string
		kind   OAuthChallengeKind
		meta   []string
		scopes []string
	}{
		{name: "invalid token", status: http.StatusUnauthorized, values: []string{`Bearer error="invalid_token", resource_metadata="https://resource.example/metadata", error_description="secret canary"`}, kind: OAuthChallengeRefresh, meta: []string{"https://resource.example/metadata"}},
		{name: "insufficient scope", status: http.StatusForbidden, values: []string{`Bearer error="insufficient_scope", scope="write read", resource_metadata="https://resource.example/metadata"`}, kind: OAuthChallengeStepUp, meta: []string{"https://resource.example/metadata"}, scopes: []string{"write", "read"}},
		{name: "case insensitive scheme", status: http.StatusUnauthorized, values: []string{`bearer error=invalid_token`}, kind: OAuthChallengeRefresh},
		{name: "wrong status", status: http.StatusForbidden, values: []string{`Bearer error="invalid_token"`}},
		{name: "unknown error", status: http.StatusUnauthorized, values: []string{`Bearer error="other"`}},
		{name: "multiple values", status: http.StatusUnauthorized, values: []string{`Bearer error="invalid_token"`, `Basic realm="x"`}},
		{name: "multiple challenges", status: http.StatusUnauthorized, values: []string{`Bearer error="invalid_token", Basic realm="x"`}},
		{name: "duplicate parameter", status: http.StatusUnauthorized, values: []string{`Bearer error="invalid_token", error="invalid_token"`}},
		{name: "malformed", status: http.StatusUnauthorized, values: []string{`Bearer error="invalid_token`}},
		{name: "oversized metadata", status: http.StatusUnauthorized, values: []string{`Bearer error="invalid_token", resource_metadata="https://example.test/` + strings.Repeat("x", 8192) + `"`}},
		{name: "too many scopes", status: http.StatusForbidden, values: []string{`Bearer error="insufficient_scope", scope="` + strings.Repeat("a ", 64) + `a"`}},
		{name: "oversized scope", status: http.StatusForbidden, values: []string{`Bearer error="insufficient_scope", scope="` + strings.Repeat("a", 257) + `"`}},
		{name: "invalid scope", status: http.StatusForbidden, values: []string{"Bearer error=\"insufficient_scope\", scope=\"read\twrite\""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			disposition := projectOAuthChallenge(test.status, test.values)
			if test.kind == "" {
				assert.Nil(t, disposition)
				return
			}
			require.NotNil(t, disposition)
			assert.Equal(t, test.kind, disposition.Kind)
			assert.Equal(t, test.meta, disposition.Metadata)
			assert.Equal(t, test.scopes, disposition.Scopes)
			assert.NotContains(t, disposition.Error(), "secret canary")
		})
	}
}
