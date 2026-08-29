//go:build integration

package invocation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvocationReadPaginationIntegration(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(3))
	older := insertReadFixture(t, repository, Admission{
		PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef",
		CredentialRevision: "1", Class: contract.AdmissionInvalidParams,
	}, nil)
	newer := insertReadFixture(t, repository, testEvaluatedAdmission(), pointer(contract.TerminalSucceeded))

	page, err := repository.List(context.Background(), contract.InvocationListQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, newer.InvocationID, page.Items[0].ID)
	require.NotNil(t, page.NextCursor)
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "redacted_arguments")

	continued, err := repository.List(context.Background(), contract.InvocationListQuery{Limit: 1, Cursor: page.NextCursor})
	require.NoError(t, err)
	require.Len(t, continued.Items, 1)
	assert.Equal(t, older.InvocationID, continued.Items[0].ID)
	assert.Nil(t, continued.NextCursor)

	item, err := repository.Get(context.Background(), newer.InvocationID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":1e0}`, string(item.RedactedArguments))
}
