//go:build integration

package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type detailNames struct {
	server, grant   *string
	err             error
	servers, grants []string
	deadline        time.Time
}

func (names *detailNames) InvocationServerName(ctx context.Context, id string) (*string, error) {
	names.servers = append(names.servers, id)
	names.deadline, _ = ctx.Deadline()
	return names.server, names.err
}
func (names *detailNames) InvocationGrantDescription(ctx context.Context, id string) (*string, error) {
	names.grants = append(names.grants, id)
	return names.grant, names.err
}

func TestInvocationDetailRecognitionIsCurrentSeparateAndOptionalIntegration(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(4))
	prepared := insertReadFixture(t, repository, testEvaluatedAdmission(), pointer(contract.TerminalSucceeded))
	original, err := repository.Get(t.Context(), prepared.InvocationID)
	require.NoError(t, err)
	before, err := json.Marshal(original)
	require.NoError(t, err)
	names := &detailNames{server: pointer("Current server"), grant: pointer("Current grant")}
	reader, err := NewReadService(repository, &readNames{}, names, names)
	require.NoError(t, err)
	for _, tc := range []struct {
		server, grant *string
		err           error
	}{
		{pointer("Current server"), pointer("Current grant"), nil},
		{pointer("Renamed server"), pointer("Renamed grant"), nil},
		{pointer("Renamed server"), pointer(""), nil},
		{nil, nil, nil},
		{pointer("Hidden server"), pointer("Hidden grant"), errors.New("unavailable")},
		{pointer(strings.Repeat("x", 257)), pointer(strings.Repeat("x", 257)), nil},
	} {
		names.server, names.grant, names.err = tc.server, tc.grant, tc.err
		names.servers, names.grants = nil, nil
		started := time.Now()
		item, err := reader.Get(t.Context(), prepared.InvocationID)
		require.NoError(t, err)
		require.NotNil(t, item.Recognition)
		require.Equal(t, []string{original.Target.ServerID}, names.servers)
		require.Equal(t, []string{*original.Authorization.GrantID}, names.grants)
		require.WithinDuration(t, started.Add(500*time.Millisecond), names.deadline, 100*time.Millisecond)
		if tc.err == nil && tc.server != nil && len(*tc.server) <= 256 {
			require.Equal(t, &contract.InvocationServerRecognition{ServerID: original.Target.ServerID, DisplayName: *tc.server}, item.Recognition.Server)
			require.Equal(t, &contract.InvocationGrantRecognition{GrantID: *original.Authorization.GrantID, Description: *tc.grant}, item.Recognition.Grant)
		} else {
			require.Nil(t, item.Recognition.Server)
			require.Nil(t, item.Recognition.Grant)
		}
		item.Recognition = nil
		after, err := json.Marshal(item)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after), "every retained projection byte is unchanged")
	}
	stored, err := repository.Get(t.Context(), prepared.InvocationID)
	require.NoError(t, err)
	require.Equal(t, original, stored)
	names.servers, names.grants = nil, nil
	page, err := reader.List(t.Context(), contract.InvocationListQuery{Limit: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Empty(t, names.servers)
	require.Empty(t, names.grants)
	_, err = reader.Get(t.Context(), invocationID(99))
	require.Error(t, err)
	require.Empty(t, names.servers)
	require.Empty(t, names.grants)
}

func TestInvocationRecognitionDoesNotInventRelationshipsIntegration(t *testing.T) {
	repository, _, _ := newInvocationRepository(t, nil, uniqueInvocationEntropy(4))
	names := &detailNames{server: pointer("Never used"), grant: pointer("Never used")}
	reader, err := NewReadService(repository, &readNames{}, names, names)
	require.NoError(t, err)
	unresolved := insertReadFixture(t, repository, Admission{Admission: activity.Admission{
		PrincipalID: invocationID(1), CredentialID: invocationID(2), CredentialFingerprint: "0123456789abcdef", CredentialRevision: "1", Class: contract.AdmissionInvalidParams,
	}}, nil)
	unresolvedItem, err := reader.Get(t.Context(), unresolved.InvocationID)
	require.NoError(t, err)
	require.Equal(t, &contract.InvocationRecognition{}, unresolvedItem.Recognition)
	admission := testEvaluatedAdmission()
	admission.MCP.Route.Target.ServerID = contract.SyntheticServerID
	admission.Authorization.GrantID = nil
	admission.Authorization.Decision = contract.DecisionBlock
	prepared := insertReadFixture(t, repository, admission, nil)
	item, err := reader.Get(t.Context(), prepared.InvocationID)
	require.NoError(t, err)
	require.Nil(t, item.Recognition.Server)
	require.Nil(t, item.Recognition.Grant)
	require.Empty(t, names.servers)
	require.Empty(t, names.grants)
}
