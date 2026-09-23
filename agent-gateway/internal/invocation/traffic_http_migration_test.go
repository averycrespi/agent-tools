package invocation

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reconstruct exactly the released schema on a disposable populated fixture.
// No migration under test is used to manufacture the old tables or rows.
func legacyTrafficFixture(t *testing.T) (*TrafficStore, func(func(string) error) (*TrafficStore, error)) {
	t.Helper()
	s, owner := trafficFixture(t, nil, nil)
	receipt, err := s.Admit(t.Context(), trafficPrepared(1))
	require.NoError(t, err)
	require.True(t, s.Confirm(t.Context(), receipt))
	completion := trafficCompletion()
	completion.Class = contract.TerminalDownstreamFailure
	require.NoError(t, s.complete(t.Context(), receipt, completion, &contract.FailureDiagnostics{GatewayObserved: contract.FailureObservation{Source: "protocol", Reason: "rpc_error"}}))
	_, err = s.db.ExecContext(t.Context(), `DROP TABLE http_traffic; PRAGMA user_version=1`)
	require.NoError(t, err)
	require.NoError(t, trafficCheckpoint(t.Context(), s.db))
	require.NoError(t, s.Close())
	return s, func(fault func(string) error) (*TrafficStore, error) {
		return openTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config, false, fault)
	}
}

func TestTrafficHTTPAdditiveUpgradePreservesGenerationAndEvidence(t *testing.T) {
	old, reopen := legacyTrafficFixture(t)
	inode, err := os.Stat(old.path)
	require.NoError(t, err)
	require.NoError(t, VerifyTrafficFile(t.Context(), old.path, invocationTestInstallationID, invocationID(90), old.config))
	upgraded, err := reopen(nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, upgraded.Close()) }()
	after, err := os.Stat(old.path)
	require.NoError(t, err)
	assert.True(t, os.SameFile(inode, after), "additive upgrade must not replace the generation inode")
	var version, count int
	require.NoError(t, upgraded.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version))
	require.Equal(t, 2, version)
	require.NoError(t, upgraded.db.QueryRowContext(t.Context(), `SELECT count(*) FROM http_traffic`).Scan(&count))
	assert.Zero(t, count)
	history, err := upgraded.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	assert.Equal(t, invocationID(90), history.Generation)
	assert.Equal(t, int64(1), history.HighWater)
	assert.Zero(t, history.Pruning)
	assert.Equal(t, invocationID(1), history.Records[0].InvocationID)
	require.NotNil(t, history.Records[0].Diagnostics)
	assert.Equal(t, "rpc_error", history.Records[0].Diagnostics.GatewayObserved.Reason)
	require.NoError(t, upgraded.Close())
	again, err := reopen(func(point string) error {
		if point == "http_migration_begin" {
			t.Error("current schema must not migrate again")
		}
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, again.Close())
}

func TestTrafficHTTPStartupRejectsCorruptEvidence(t *testing.T) {
	for _, mode := range []string{"decision", "charge", "cross-domain identity"} {
		t.Run(mode, func(t *testing.T) {
			s, owner := trafficFixture(t, nil, nil)
			receipt, err := s.Admit(t.Context(), trafficPrepared(1))
			require.NoError(t, err)
			s.Release(receipt)
			admission := httpTrafficAdmission(2)
			if mode == "cross-domain identity" {
				admission.ID = invocationID(1)
			}
			raw, err := encodeHTTPAdmission(admission)
			require.NoError(t, err)
			if mode == "decision" {
				raw = strings.Replace(raw, `"allowed":true`, `"allowed":false`, 1)
			}
			charge := int64(len(raw)) + httpTrafficChargeBase
			if mode == "charge" {
				charge++
			}
			_, err = s.db.ExecContext(t.Context(), `INSERT INTO http_traffic(insertion_sequence,id,admission,bytes) VALUES(2,?,?,?)`, admission.ID, raw, charge)
			require.NoError(t, err)
			_, err = s.db.ExecContext(t.Context(), `UPDATE traffic_meta SET high_water=2`)
			require.NoError(t, err)
			_, err = s.db.ExecContext(t.Context(), `UPDATE sqlite_sequence SET seq=2 WHERE name='invocations'`)
			require.NoError(t, err)
			require.NoError(t, s.Close())
			reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
			require.Error(t, err)
			require.Nil(t, reopened)
		})
	}
}

func TestTrafficHTTPUpgradeFailureNeverReadiesPartialSchema(t *testing.T) {
	for _, point := range []string{"http_migration_begin", "http_migration_commit", "http_migration_acknowledgment"} {
		t.Run(point, func(t *testing.T) {
			_, reopen := legacyTrafficFixture(t)
			failed, err := reopen(func(at string) error {
				if at == point {
					return errors.New("injected migration failure")
				}
				return nil
			})
			require.Error(t, err)
			require.Nil(t, failed)
			// A fresh startup completely validates whichever atomic version settled.
			recovered, err := reopen(nil)
			require.NoError(t, err)
			defer func() { require.NoError(t, recovered.Close()) }()
			history, err := recovered.History(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			require.NotNil(t, history.Records[0].Diagnostics)
		})
	}
}
