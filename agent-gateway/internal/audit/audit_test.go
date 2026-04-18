package audit_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
)

// openTestDB opens a temp SQLite database with all migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ptr helpers for nullable strings and ints.
func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// TestLogger_RecordAndQuery verifies the 8 representative rows from §5.
func TestLogger_RecordAndQuery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	tests := []struct {
		name  string
		entry audit.Entry
	}{
		{
			// Row 1: tunnel row — no MITM, method/path NULL.
			name: "tunnel",
			entry: audit.Entry{
				ID:              "01HZ000000000000000000001",
				TS:              now,
				Agent:           strPtr("agent1"),
				Interception:    "tunnel",
				Method:          nil,
				Host:            "tunnel.example.com",
				Path:            nil,
				Query:           nil,
				Status:          nil,
				DurationMS:      5,
				BytesIn:         100,
				BytesOut:        200,
				MatchedRule:     nil,
				RuleVerdict:     nil,
				Approval:        nil,
				Injection:       nil,
				Outcome:         "forwarded",
				CredentialRef:   nil,
				CredentialScope: nil,
				Error:           nil,
			},
		},
		{
			// Row 2: MITM no-rule — matched_rule NULL, forwarded.
			name: "mitm-no-rule",
			entry: audit.Entry{
				ID:              "01HZ000000000000000000002",
				TS:              now,
				Agent:           strPtr("agent1"),
				Interception:    "mitm",
				Method:          strPtr("GET"),
				Host:            "api.example.com",
				Path:            strPtr("/unmatched"),
				Query:           nil,
				Status:          intPtr(200),
				DurationMS:      12,
				BytesIn:         50,
				BytesOut:        300,
				MatchedRule:     nil,
				RuleVerdict:     nil,
				Approval:        nil,
				Injection:       nil,
				Outcome:         "forwarded",
				CredentialRef:   nil,
				CredentialScope: nil,
				Error:           nil,
			},
		},
		{
			// Row 3: happy-path allow — injection='applied', credential set.
			name: "allow-applied",
			entry: audit.Entry{
				ID:              "01HZ000000000000000000003",
				TS:              now,
				Agent:           strPtr("agent1"),
				Interception:    "mitm",
				Method:          strPtr("GET"),
				Host:            "api.github.com",
				Path:            strPtr("/repos"),
				Query:           nil,
				Status:          intPtr(200),
				DurationMS:      30,
				BytesIn:         100,
				BytesOut:        1024,
				MatchedRule:     strPtr("github-issues"),
				RuleVerdict:     strPtr("allow"),
				Approval:        nil,
				Injection:       strPtr("applied"),
				Outcome:         "forwarded",
				CredentialRef:   strPtr("gh_bot"),
				CredentialScope: strPtr("global"),
				Error:           nil,
			},
		},
		{
			// Row 4: fail-soft allow — injection='failed', error='secret_unresolved'.
			name: "allow-failed",
			entry: audit.Entry{
				ID:              "01HZ000000000000000000004",
				TS:              now,
				Agent:           strPtr("agent1"),
				Interception:    "mitm",
				Method:          strPtr("GET"),
				Host:            "api.github.com",
				Path:            strPtr("/issues"),
				Query:           nil,
				Status:          intPtr(401),
				DurationMS:      15,
				BytesIn:         80,
				BytesOut:        200,
				MatchedRule:     strPtr("github-issues"),
				RuleVerdict:     strPtr("allow"),
				Approval:        nil,
				Injection:       strPtr("failed"),
				Outcome:         "forwarded",
				CredentialRef:   nil,
				CredentialScope: nil,
				Error:           strPtr("secret_unresolved"),
			},
		},
		{
			// Row 5: deny — outcome='blocked'.
			name: "deny-blocked",
			entry: audit.Entry{
				ID:              "01HZ000000000000000000005",
				TS:              now,
				Agent:           strPtr("agent1"),
				Interception:    "mitm",
				Method:          strPtr("DELETE"),
				Host:            "api.example.com",
				Path:            strPtr("/destroy"),
				Query:           nil,
				Status:          nil,
				DurationMS:      2,
				BytesIn:         0,
				BytesOut:        0,
				MatchedRule:     strPtr("block-all-delete"),
				RuleVerdict:     strPtr("deny"),
				Approval:        nil,
				Injection:       nil,
				Outcome:         "blocked",
				CredentialRef:   nil,
				CredentialScope: nil,
				Error:           nil,
			},
		},
		{
			// Row 6: approved — approval='approved', injection='applied'.
			name: "approved-applied",
			entry: audit.Entry{
				ID:              "01HZ000000000000000000006",
				TS:              now,
				Agent:           strPtr("agent1"),
				Interception:    "mitm",
				Method:          strPtr("POST"),
				Host:            "deploy.example.com",
				Path:            strPtr("/release"),
				Query:           nil,
				Status:          intPtr(200),
				DurationMS:      5000,
				BytesIn:         200,
				BytesOut:        100,
				MatchedRule:     strPtr("prod-deploy"),
				RuleVerdict:     strPtr("require-approval"),
				Approval:        strPtr("approved"),
				Injection:       strPtr("applied"),
				Outcome:         "forwarded",
				CredentialRef:   strPtr("deploy_key"),
				CredentialScope: strPtr("agent:agent1"),
				Error:           nil,
			},
		},
		{
			// Row 7: denied by approver — approval='denied', outcome='blocked'.
			name: "approval-denied",
			entry: audit.Entry{
				ID:              "01HZ000000000000000000007",
				TS:              now,
				Agent:           strPtr("agent1"),
				Interception:    "mitm",
				Method:          strPtr("POST"),
				Host:            "deploy.example.com",
				Path:            strPtr("/release"),
				Query:           nil,
				Status:          nil,
				DurationMS:      3000,
				BytesIn:         200,
				BytesOut:        0,
				MatchedRule:     strPtr("prod-deploy"),
				RuleVerdict:     strPtr("require-approval"),
				Approval:        strPtr("denied"),
				Injection:       nil,
				Outcome:         "blocked",
				CredentialRef:   nil,
				CredentialScope: nil,
				Error:           nil,
			},
		},
		{
			// Row 8: timed-out — approval='timed-out', outcome='blocked'.
			name: "approval-timeout",
			entry: audit.Entry{
				ID:              "01HZ000000000000000000008",
				TS:              now,
				Agent:           strPtr("agent1"),
				Interception:    "mitm",
				Method:          strPtr("POST"),
				Host:            "deploy.example.com",
				Path:            strPtr("/release"),
				Query:           nil,
				Status:          nil,
				DurationMS:      30000,
				BytesIn:         200,
				BytesOut:        0,
				MatchedRule:     strPtr("prod-deploy"),
				RuleVerdict:     strPtr("require-approval"),
				Approval:        strPtr("timed-out"),
				Injection:       nil,
				Outcome:         "blocked",
				CredentialRef:   nil,
				CredentialScope: nil,
				Error:           nil,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			logger := audit.NewLogger(db)
			ctx := context.Background()

			require.NoError(t, logger.Record(ctx, tc.entry))

			entries, err := logger.Query(ctx, audit.Filter{})
			require.NoError(t, err)
			require.Len(t, entries, 1)

			got := entries[0]
			assert.Equal(t, tc.entry.ID, got.ID)
			assert.Equal(t, tc.entry.Interception, got.Interception)
			assert.Equal(t, tc.entry.Outcome, got.Outcome)
			assert.Equal(t, tc.entry.Method, got.Method)
			assert.Equal(t, tc.entry.Path, got.Path)
			assert.Equal(t, tc.entry.MatchedRule, got.MatchedRule)
			assert.Equal(t, tc.entry.RuleVerdict, got.RuleVerdict)
			assert.Equal(t, tc.entry.Approval, got.Approval)
			assert.Equal(t, tc.entry.Injection, got.Injection)
			assert.Equal(t, tc.entry.CredentialRef, got.CredentialRef)
			assert.Equal(t, tc.entry.CredentialScope, got.CredentialScope)
			assert.Equal(t, tc.entry.Error, got.Error)
		})
	}
}

// TestLogger_QueryFilter verifies that Filter fields narrow results correctly.
func TestLogger_QueryFilter(t *testing.T) {
	db := openTestDB(t)
	logger := audit.NewLogger(db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	entries := []audit.Entry{
		{
			ID: "01HZ0A0000000000000000001", TS: base.Add(-2 * time.Minute),
			Agent: strPtr("agent-a"), Interception: "mitm", Host: "alpha.example.com",
			Method: strPtr("GET"), Path: strPtr("/a"), DurationMS: 1, BytesIn: 0, BytesOut: 0,
			MatchedRule: strPtr("rule-a"), Outcome: "forwarded",
		},
		{
			ID: "01HZ0A0000000000000000002", TS: base.Add(-1 * time.Minute),
			Agent: strPtr("agent-b"), Interception: "mitm", Host: "beta.example.com",
			Method: strPtr("POST"), Path: strPtr("/b"), DurationMS: 2, BytesIn: 0, BytesOut: 0,
			MatchedRule: strPtr("rule-b"), Outcome: "blocked",
		},
	}
	for _, e := range entries {
		require.NoError(t, logger.Record(ctx, e))
	}

	t.Run("by agent", func(t *testing.T) {
		got, err := logger.Query(ctx, audit.Filter{Agent: strPtr("agent-a")})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "agent-a", *got[0].Agent)
	})

	t.Run("by host", func(t *testing.T) {
		got, err := logger.Query(ctx, audit.Filter{Host: strPtr("beta.example.com")})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "beta.example.com", got[0].Host)
	})

	t.Run("by rule", func(t *testing.T) {
		got, err := logger.Query(ctx, audit.Filter{Rule: strPtr("rule-a")})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "rule-a", *got[0].MatchedRule)
	})

	t.Run("time-range", func(t *testing.T) {
		after := base.Add(-90 * time.Second)
		got, err := logger.Query(ctx, audit.Filter{After: &after})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "01HZ0A0000000000000000002", got[0].ID)
	})

	t.Run("limit and offset", func(t *testing.T) {
		lim := 1
		got, err := logger.Query(ctx, audit.Filter{Limit: &lim})
		require.NoError(t, err)
		require.Len(t, got, 1)

		off := 1
		got2, err := logger.Query(ctx, audit.Filter{Limit: &lim, Offset: &off})
		require.NoError(t, err)
		require.Len(t, got2, 1)
		assert.NotEqual(t, got[0].ID, got2[0].ID)
	})
}

// TestLogger_Prune verifies rows strictly before the cutoff are removed.
func TestLogger_Prune(t *testing.T) {
	db := openTestDB(t)
	logger := audit.NewLogger(db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	old := audit.Entry{
		ID: "01HZ0B0000000000000000001", TS: base.Add(-2 * time.Hour),
		Interception: "mitm", Host: "old.example.com",
		Method: strPtr("GET"), Path: strPtr("/old"), DurationMS: 1, BytesIn: 0, BytesOut: 0,
		Outcome: "forwarded",
	}
	new_ := audit.Entry{
		ID: "01HZ0B0000000000000000002", TS: base,
		Interception: "mitm", Host: "new.example.com",
		Method: strPtr("GET"), Path: strPtr("/new"), DurationMS: 1, BytesIn: 0, BytesOut: 0,
		Outcome: "forwarded",
	}
	require.NoError(t, logger.Record(ctx, old))
	require.NoError(t, logger.Record(ctx, new_))

	// Cutoff is exactly the boundary: rows strictly before are pruned.
	cutoff := base.Add(-1 * time.Hour)
	n, err := logger.Prune(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly the old row should be pruned")

	// Verify the remaining row is the new one.
	remaining, err := logger.Query(ctx, audit.Filter{})
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, new_.ID, remaining[0].ID)
}

// TestLogger_Prune_NotAtCutoff verifies a row AT the cutoff is NOT pruned (strict <).
func TestLogger_Prune_NotAtCutoff(t *testing.T) {
	db := openTestDB(t)
	logger := audit.NewLogger(db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	e := audit.Entry{
		ID: "01HZ0C0000000000000000001", TS: base,
		Interception: "mitm", Host: "at-cutoff.example.com",
		DurationMS: 1, BytesIn: 0, BytesOut: 0,
		Outcome: "forwarded",
	}
	require.NoError(t, logger.Record(ctx, e))

	// Prune with cutoff = exactly e.TS: row must survive (strict <, not <=).
	n, err := logger.Prune(ctx, base)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// TestLogger_CredentialRefInvariant verifies that credential_ref IS NOT NULL
// if and only if injection = 'applied', and that credential_scope agrees with
// credential_ref (both set or both nil).
func TestLogger_CredentialRefInvariant(t *testing.T) {
	db := openTestDB(t)
	logger := audit.NewLogger(db)
	ctx := context.Background()

	// Valid: injection='applied' + credential_ref + credential_scope all set.
	valid := audit.Entry{
		ID: "01HZ0D0000000000000000001", TS: time.Now(),
		Interception: "mitm", Host: "ok.example.com",
		DurationMS: 1, BytesIn: 0, BytesOut: 0,
		Injection:       strPtr("applied"),
		CredentialRef:   strPtr("gh_bot"),
		CredentialScope: strPtr("global"),
		Outcome:         "forwarded",
	}
	require.NoError(t, logger.Record(ctx, valid))

	// Invalid: injection='applied' but credential_ref nil → must fail.
	missing := audit.Entry{
		ID: "01HZ0D0000000000000000002", TS: time.Now(),
		Interception: "mitm", Host: "bad.example.com",
		DurationMS: 1, BytesIn: 0, BytesOut: 0,
		Injection: strPtr("applied"), CredentialRef: nil,
		Outcome: "forwarded",
	}
	require.ErrorIs(t, logger.Record(ctx, missing), audit.ErrInvariantViolation)

	// Invalid: injection=nil but credential_ref set → must fail.
	orphan := audit.Entry{
		ID: "01HZ0D0000000000000000003", TS: time.Now(),
		Interception: "mitm", Host: "bad2.example.com",
		DurationMS: 1, BytesIn: 0, BytesOut: 0,
		Injection: nil, CredentialRef: strPtr("gh_bot"),
		Outcome: "forwarded",
	}
	require.ErrorIs(t, logger.Record(ctx, orphan), audit.ErrInvariantViolation)

	// Invalid: credential_ref set but credential_scope nil → must fail.
	scopeMissing := audit.Entry{
		ID: "01HZ0D0000000000000000004", TS: time.Now(),
		Interception: "mitm", Host: "bad3.example.com",
		DurationMS: 1, BytesIn: 0, BytesOut: 0,
		Injection:       strPtr("applied"),
		CredentialRef:   strPtr("gh_bot"),
		CredentialScope: nil,
		Outcome:         "forwarded",
	}
	require.Error(t, logger.Record(ctx, scopeMissing), "ref without scope must fail")

	// Invalid: credential_scope set but credential_ref nil → must fail.
	refMissing := audit.Entry{
		ID: "01HZ0D0000000000000000005", TS: time.Now(),
		Interception: "mitm", Host: "bad4.example.com",
		DurationMS: 1, BytesIn: 0, BytesOut: 0,
		Injection:       nil,
		CredentialRef:   nil,
		CredentialScope: strPtr("global"),
		Outcome:         "forwarded",
	}
	require.Error(t, logger.Record(ctx, refMissing), "scope without ref must fail")
}
