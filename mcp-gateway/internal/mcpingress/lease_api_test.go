package mcpingress

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/stretchr/testify/assert"
)

func TestAgentAuthenticatorUsesRegisteredAuthorizationLease(t *testing.T) {
	var _ AgentAuthenticator = (*authorization.Repository)(nil)
	lease, ok := LeaseFromContext(context.Background())
	assert.False(t, ok)
	assert.Nil(t, lease)
}
