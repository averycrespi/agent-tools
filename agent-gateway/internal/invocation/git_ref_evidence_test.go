package invocation

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/stretchr/testify/require"
)

func TestGitRefEvidenceRoundTripAndLegacy(t *testing.T) {
	s, owner := trafficFixture(t, nil, nil)
	legacy := gitTrafficAdmission(1)
	recordGit(t, s, legacy)
	a := gitTrafficAdmission(2)
	a.Commands = 3
	a.RefEvidence = gitpolicy.TrafficRefs([]gitpolicy.RefAction{{Ref: "refs/heads/new", Action: "create"}, {Ref: "refs/heads/main", Action: "update"}, {Ref: "refs/tags/old", Action: "delete"}})
	c := gitTrafficCompletion()
	c.ReportedResult = "reported_partial"
	c.RefOutcomes = []string{"ok", "ng", "ok"}
	recordGitCompletion(t, s, recordGit(t, s, a), c)
	denied := a
	denied.ID = gitTrafficAdmission(3).ID
	denied.Allowed = false
	recordGit(t, s, denied)
	require.NoError(t, s.Close())
	reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	h, err := reopened.GitHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 3)
	require.Nil(t, h.Records[0].Admission.RefEvidence)
	require.Equal(t, a.RefEvidence, h.Records[1].Admission.RefEvidence)
	require.Equal(t, c.RefOutcomes, h.Records[1].Completion.RefOutcomes)
	require.Equal(t, denied, h.Records[2].Admission)
	require.Nil(t, h.Records[2].Completion)
}

func TestGitRefEvidenceBudgetsAndValidation(t *testing.T) {
	a := gitTrafficAdmission(1)
	a.Commands = 8
	actions := make([]gitpolicy.RefAction, 8)
	for i := range actions {
		actions[i] = gitpolicy.RefAction{Ref: "refs/heads/" + strings.Repeat(string(rune('a'+i)), 100), Action: "update"}
	}
	a.RefEvidence = gitpolicy.TrafficRefs(actions)
	raw, err := encodeGitAdmission(a)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), 8192)
	c := gitTrafficCompletion()
	c.ReportedResult = "reported_success"
	c.RefOutcomes = []string{"ok", "ok", "ok", "ok", "ok", "ok", "ok", "ok"}
	c.BytesSent = math.MaxInt64
	c.BytesReceived = math.MaxInt64
	c.DurationMS = math.MaxInt64
	terminal, err := encodeGitCompletion(a, c)
	require.NoError(t, err)
	require.LessOrEqual(t, len(terminal), 512)
	for _, mutate := range []func(*contract.GitTrafficAdmission){
		func(a *contract.GitTrafficAdmission) { a.RefEvidence.Refs[0].Name = "refs/heads/bad\nsecret" },
		func(a *contract.GitTrafficAdmission) { a.RefEvidence.Refs[0].Action = "secret" },
		func(a *contract.GitTrafficAdmission) { a.RefEvidence.Refs[1] = a.RefEvidence.Refs[0] },
		func(a *contract.GitTrafficAdmission) { a.RefEvidence.State = "complete"; a.Commands = 9 },
	} {
		var invalid contract.GitTrafficAdmission
		require.NoError(t, json.Unmarshal([]byte(raw), &invalid))
		mutate(&invalid)
		_, err := encodeGitAdmission(invalid)
		require.Error(t, err)
	}
	for _, mutate := range []func(*contract.GitTrafficCompletion){
		func(c *contract.GitTrafficCompletion) { c.RefOutcomes[0] = "raw secret message" },
		func(c *contract.GitTrafficCompletion) { c.RefOutcomes = c.RefOutcomes[:1] },
		func(c *contract.GitTrafficCompletion) { c.Status = 403 },
		func(c *contract.GitTrafficCompletion) { c.TransferComplete = false },
		func(c *contract.GitTrafficCompletion) { c.ReportedResult = "" },
		func(c *contract.GitTrafficCompletion) { c.RefOutcomes[0] = "ng" },
	} {
		var invalid contract.GitTrafficCompletion
		require.NoError(t, json.Unmarshal([]byte(terminal), &invalid))
		mutate(&invalid)
		_, err := encodeGitCompletion(a, invalid)
		require.Error(t, err)
	}
}
