package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminSessionBootstrapContract(t *testing.T) {
	route, ok := RouteForPath("/api/v1/admin-sessions/current")
	require.True(t, ok)
	assert.Equal(t, []string{"DELETE", "POST"}, route.Methods)
	assert.Equal(t, "DELETE, POST", route.Allow())
	assert.Equal(t, AuthorityAdminSession, route.Authority)

	var post *ResourceMechanic
	for _, mechanic := range ResourceMechanics() {
		if mechanic.Pattern == route.Pattern && mechanic.Method == "POST" {
			copy := mechanic
			post = &copy
		}
	}
	require.NotNil(t, post)
	assert.Equal(t, "EmptyObject", post.RequestSchema)
	assert.Equal(t, "AdminSessionBootstrap", post.SuccessSchema)
	assert.Equal(t, []int{200}, post.SuccessStatuses)

	encoded, err := json.Marshal(AdminSessionBootstrap{CSRFToken: "csrf", IdleExpiresAt: "2026-08-28T16:30:00Z", AbsoluteExpiresAt: "2026-08-29T00:00:00Z"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"csrf_token":"csrf","idle_expires_at":"2026-08-28T16:30:00Z","absolute_expires_at":"2026-08-29T00:00:00Z"}`, string(encoded))
}
