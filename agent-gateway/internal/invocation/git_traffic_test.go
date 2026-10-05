package invocation

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func gitTrafficAdmission(id int) contract.GitTrafficAdmission {
	p := trafficPrepared(id)
	return contract.GitTrafficAdmission{ID: p.InvocationID, AdmittedAt: p.AdmittedAt, EvaluatedAt: p.admission.Authorization.EvaluatedAt, Principal: contract.GitRevisionRef{ID: p.admission.PrincipalID, Revision: "1"}, AgentCredential: contract.GitRevisionRef{ID: p.admission.CredentialID, Revision: "1"}, Repository: contract.GitRevisionRef{ID: invocationID(70), Revision: "1"}, AliasRevision: "1", ProfileRevision: "1", AuthorizationRevision: "1", Operation: "push", Commands: 1, Allowed: true}
}
func gitTrafficCompletion() contract.GitTrafficCompletion {
	return contract.GitTrafficCompletion{CompletedAt: trafficCompletion().CompletedAt, Outcome: "outcome_unknown", Status: 200, TransferComplete: true}
}
func TestGitTrafficUncertaintyNeverReplays(t *testing.T) {
	for _, point := range []string{"before_begin", "statement", "commit", "rollback", "acknowledgment"} {
		t.Run(point, func(t *testing.T) {
			s, owner := trafficFixture(t, nil, func(at string) error {
				if at == point || point == "rollback" && at == "statement" {
					return errors.New("injected")
				}
				return nil
			})
			require.NotNil(t, s.ObserveGit(gitTrafficAdmission(1)))
			waitTraffic(t, s)
			require.False(t, s.Healthy())
			history, err := s.GitHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			expected := 0
			if point == "acknowledgment" {
				expected = 1
			}
			require.Len(t, history.Records, expected)
			require.NoError(t, s.Close())
			reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			history, err = reopened.GitHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, expected)
			if expected == 1 {
				require.Nil(t, history.Records[0].Completion)
			}
		})
	}
}
func TestGitTrafficSharedRetention(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 2; c.BatchRecords = 1 }, nil)
	first := recordGit(t, s, gitTrafficAdmission(1))
	recordHTTP(t, s, httpTrafficAdmission(2))
	recordMCP(t, s, trafficPrepared(3))
	recordGitCompletion(t, s, first, gitTrafficCompletion())
	hh, err := s.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Empty(t, hh.Records)
	h, err := s.GitHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.EqualValues(t, 4, h.HighWater)
	require.EqualValues(t, 2, h.Pruning)
	require.Equal(t, "outcome_unknown", h.Records[0].Completion.Outcome)
}
func TestGitTrafficLateRowValidation(t *testing.T) {
	for _, mode := range []string{"revision", "unknown", "duplicate", "charge", "success", "collision"} {
		t.Run(mode, func(t *testing.T) {
			s, owner := trafficFixture(t, nil, nil)
			recordHTTP(t, s, httpTrafficAdmission(1))
			a := gitTrafficAdmission(2)
			if mode == "collision" {
				a.ID = invocationID(1)
			}
			raw, err := encodeGitAdmission(a)
			require.NoError(t, err)
			var terminal any
			switch mode {
			case "revision":
				raw = strings.Replace(raw, `"alias_revision":"1"`, `"alias_revision":"01"`, 1)
			case "unknown":
				raw = strings.TrimSuffix(raw, "}") + `,"ref":"refs/heads/private"}`
			case "duplicate":
				raw = strings.TrimSuffix(raw, "}") + `,"allowed":true}`
			case "success":
				completion := gitTrafficCompletion()
				value, _ := encodeGitCompletion(a, completion)
				terminal = strings.Replace(value, "outcome_unknown", "succeeded", 1)
			}
			charge := gitTrafficChargeBase + int64(len(raw))
			if mode == "charge" {
				charge++
			}
			_, err = s.db.ExecContext(t.Context(), `INSERT INTO git_traffic(insertion_sequence,id,admission,completion,bytes) VALUES(2,?,?,?,?)`, a.ID, raw, terminal, charge)
			require.NoError(t, err)
			_, err = s.db.ExecContext(t.Context(), `UPDATE traffic_meta SET high_water=2; UPDATE sqlite_sequence SET seq=2 WHERE name='invocations'`)
			require.NoError(t, err)
			require.NoError(t, s.Close())
			reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
			require.Error(t, err)
			require.Nil(t, reopened)
		})
	}
}
func TestGitTrafficPairedRestorePreservesAllDomains(t *testing.T) {
	s, owner := trafficFixture(t, nil, nil)
	control, err := storage.Initialize(t.Context(), owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	require.NoError(t, control.SelectTraffic(t.Context(), "", invocationID(90)))
	recordMCP(t, s, trafficPrepared(1))
	recordHTTP(t, s, httpTrafficAdmission(2))
	g := recordGit(t, s, gitTrafficAdmission(3))
	recordGitCompletion(t, s, g, gitTrafficCompletion())
	recordGit(t, s, gitTrafficAdmission(4))
	root := t.TempDir()
	source := filepath.Join(root, "traffic.db")
	s.writerGate.Lock()
	busy := s.BackupPair(t.Context(), control, filepath.Join(root, "control.db"), source)
	s.writerGate.Unlock()
	require.ErrorIs(t, busy, ErrTrafficCapacity)
	require.NoError(t, s.BackupPair(t.Context(), control, filepath.Join(root, "control.db"), source))
	require.NoError(t, s.Close())
	require.NoError(t, VerifyTrafficFile(t.Context(), source, invocationTestInstallationID, invocationID(90), s.config))
	require.NoError(t, RestoreTraffic(t.Context(), owner, source, invocationTestInstallationID, invocationID(90), invocationID(91), s.config))
	restored, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(91), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, restored.Close()) }()
	history, err := restored.GitHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 2)
	require.Equal(t, gitTrafficCompletion(), *history.Records[0].Completion)
	require.Nil(t, history.Records[1].Completion)
	require.Equal(t, int64(4), history.HighWater)
	mh, err := restored.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, mh.Records, 1)
	hh, err := restored.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, hh.Records, 1)
}
func TestGitTrafficSchemaTwoUpgrade(t *testing.T) {
	s, owner := trafficFixture(t, nil, nil)
	recordHTTP(t, s, httpTrafficAdmission(1))
	_, err := s.db.ExecContext(t.Context(), `DROP TABLE git_traffic; PRAGMA user_version=2`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, VerifyTrafficFile(t.Context(), s.path, invocationTestInstallationID, invocationID(90), s.config))
	reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	history, err := reopened.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	git, err := reopened.GitHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Empty(t, git.Records)
}
