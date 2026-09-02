package contract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserEventPostContract(t *testing.T) {
	route, ok := RouteForPath("/api/v1/events")
	require.True(t, ok)
	assert.Equal(t, []string{"GET", "POST"}, route.Methods)
	assert.Equal(t, "GET, POST", route.Allow())
	assert.Equal(t, AuthorityAdmin, AuthorityForMethod(route, "GET"))
	assert.Equal(t, AuthorityAdminSession, AuthorityForMethod(route, "POST"))

	var post *ResourceMechanic
	for _, mechanic := range ResourceMechanics() {
		if mechanic.Pattern == route.Pattern && mechanic.Method == "POST" {
			copy := mechanic
			post = &copy
		}
	}
	require.NotNil(t, post)
	assert.Equal(t, "EmptyObject", post.RequestSchema)
	assert.Equal(t, "EventStream", post.SuccessSchema)
	assert.Equal(t, []int{200}, post.SuccessStatuses)
	assert.False(t, post.EventReplay)
}
