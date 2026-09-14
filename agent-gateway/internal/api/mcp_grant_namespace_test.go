package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestMCPGrantRoutesPreserveAuditIdentity(t *testing.T) {
	for _, test := range []struct {
		method, path, category, action, targetType, targetID string
	}{
		{"POST", "/api/v2/mcp/grants", "grant", "create", "installation", testServerID},
		{"PATCH", "/api/v2/mcp/grants/" + testID, "grant", "update", "grant", testID},
		{"DELETE", "/api/v2/mcp/grants/" + testID, "grant", "delete", "grant", testID},
		{"POST", "/api/v2/mcp/grant-requests/" + testID + "/approve", "grant_request", "approve", "grant_request", testID},
		{"POST", "/api/v2/mcp/grant-requests/" + testID + "/reject", "grant_request", "reject", "grant_request", testID},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			category, action, target, ok := auditMutationTarget(request, testServerID)
			require.True(t, ok)
			require.Equal(t, test.category, category)
			require.Equal(t, test.action, action)
			require.Equal(t, contract.AuditTarget{Type: test.targetType, ID: test.targetID}, target)
			request = httptest.NewRequest(test.method, strings.Replace(test.path, "/mcp/", "/", 1), nil)
			_, _, _, ok = auditMutationTarget(request, testServerID)
			require.False(t, ok, "retired routes must not infer an audit mutation target")
		})
	}
}
