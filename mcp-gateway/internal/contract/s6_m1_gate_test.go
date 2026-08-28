package contract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6M1Gate(t *testing.T) {
	events, ok := RouteForPath("/api/v1/events")
	require.True(t, ok)
	assert.Equal(t, "GET, POST", events.Allow())
	assert.Equal(t, AuthorityAdmin, AuthorityForMethod(events, "GET"))
	assert.Equal(t, AuthorityAdminSession, AuthorityForMethod(events, "POST"))

	bootstrap, ok := RouteForPath("/api/v1/admin-sessions/current")
	require.True(t, ok)
	assert.Equal(t, AuthorityAdminSession, bootstrap.Authority)
	assert.Equal(t, "DELETE, POST", bootstrap.Allow())

	mechanics := make(map[string]ResourceMechanic)
	for _, mechanic := range ResourceMechanics() {
		if mechanic.Pattern == bootstrap.Pattern || mechanic.Pattern == events.Pattern {
			mechanics[mechanic.Method+" "+mechanic.Pattern] = mechanic
		}
	}
	assert.Equal(t, "AdminSessionBootstrap", mechanics["POST "+bootstrap.Pattern].SuccessSchema)
	eventMechanic := mechanics["POST "+events.Pattern]
	assert.Equal(t, "EmptyObject", eventMechanic.RequestSchema)
	assert.Equal(t, "EventStream", eventMechanic.SuccessSchema)
	assert.Equal(t, []int{200}, eventMechanic.SuccessStatuses)
}
