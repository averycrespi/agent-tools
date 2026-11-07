//go:build integration

package configimport

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/configexport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationOnlineConfigurationReservationsAndDeletedCredentials(t *testing.T) {
	s, store, ctx := onlineFixture(t)
	reserved := func(kind, key string) contract.ConfigDefinition {
		d := definition(kind, key, "null")
		d.Reserved = true
		return d
	}
	graph := onlineGraph()
	defs := []contract.ConfigDefinition{graph[0], reserved("mcp_server", "deleted"), reserved("git_repository", "repo"), graph[2], graph[7],
		definition("http_credential", "old-http", `{"name":"Old HTTP","boundary":{"host":"old.example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":true,"secret_ref":null}`),
		definition("git_credential", "old-git", `{"name":"Old Git","origin":"https://old.example.com:443","recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":true,"secret_ref":null}`), reserved("mcp_grant", "old-grant")}
	_, err := s.Import(ctx, submission(t, defs...))
	require.NoError(t, err)
	for table, want := range map[string]int{"principals": 1, "servers": 0, "server_identities": 1, "git_repositories": 0, "grants": 1, "git_grants": 1, "http_credentials": 1, "git_credentials": 1, "secret_generations": 0, "server_import_activation": 0} {
		require.Equal(t, want, count(t, store, ctx, table), table)
	}
	require.NoError(t, s.authority.ValidateStartup(ctx, s.targets))
	raw, err := configexport.New(store).ExportBundle(ctx)
	require.NoError(t, err)
	target, targetStore, targetCtx := onlineFixture(t)
	_, err = target.Import(targetCtx, raw)
	require.NoError(t, err)
	again, err := configexport.New(targetStore).ExportBundle(targetCtx)
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(again))
	for _, d := range defs {
		_, err = s.Preview(ctx, submission(t, d))
		require.ErrorIs(t, err, ErrConflict)
	}
	// Key-only reservations cannot stand in for agents or credential definitions.
	bad := graph[0]
	bad.Key = "other"
	badGrant := graph[4]
	var spec contract.ConfigHTTPGrantSpec
	require.NoError(t, json.Unmarshal(badGrant.Spec, &spec))
	spec.Agent.Key = "other"
	ref := contract.ConfigReference{Kind: "http_credential", Key: "old-http"}
	spec.Policy.Credential = &ref
	badGrant.Spec, _ = json.Marshal(spec)
	_, err = s.Import(ctx, submission(t, bad, badGrant))
	require.Error(t, err)
	require.Equal(t, 1, count(t, store, ctx, "principals"))
}

func TestIntegrationOnlineConfigurationSingletonEligibilityAndRaces(t *testing.T) {
	for _, origins := range []string{`[]`, `["https://example.com:443"]`} {
		t.Run(origins, func(t *testing.T) {
			s, store, ctx := onlineFixture(t)
			routing := definition("git_routing", "", `{"origins":`+origins+`}`)
			_, err := s.Preview(ctx, submission(t, routing))
			require.NoError(t, err)
			_, err = s.Import(ctx, submission(t, routing))
			require.NoError(t, err)
			profile, err := s.authority.GetGitRoutingProfile(ctx)
			require.NoError(t, err)
			require.Equal(t, "2", profile.Revision)
			_, err = s.Import(ctx, submission(t, routing))
			require.ErrorIs(t, err, ErrConflict)
			before := count(t, store, ctx, "control_audit_events")
			_, err = s.Preview(ctx, submission(t, routing))
			require.ErrorIs(t, err, ErrConflict)
			require.Equal(t, before, count(t, store, ctx, "control_audit_events"))
		})
	}
	t.Run("normal edit and empty reset", func(t *testing.T) {
		s, _, ctx := onlineFixture(t)
		routing := definition("git_routing", "", `{"origins":[]}`)
		_, err := s.Preview(ctx, submission(t, routing))
		require.NoError(t, err)
		p, err := s.authority.PutGitRoutingProfile(ctx, "1", []string{"https://example.com"})
		require.NoError(t, err)
		_, err = s.authority.PutGitRoutingProfile(ctx, p.Revision, []string{})
		require.NoError(t, err)
		_, err = s.Import(ctx, submission(t, routing))
		require.ErrorIs(t, err, ErrConflict)
	})
	t.Run("historical receipt disqualifies revision-one empty", func(t *testing.T) {
		s, store, ctx := onlineFixture(t)
		require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO configuration_import VALUES(1,?,?)`, `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`, `bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb`)
			return err
		}))
		_, err := s.Import(ctx, submission(t, definition("git_routing", "", `{"origins":[]}`)))
		require.ErrorIs(t, err, ErrConflict)
		_, err = s.Import(ctx, submission(t, onlineGraph()[0]))
		require.NoError(t, err)
		p, err := s.authority.GetGitRoutingProfile(ctx)
		require.NoError(t, err)
		require.Equal(t, "1", p.Revision)
	})
	t.Run("rollback after all owners and routing", func(t *testing.T) {
		s, store, ctx := onlineFixture(t)
		raw := submission(t, onlineGraph()...)
		before := count(t, store, ctx, "control_audit_events")
		require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `CREATE TRIGGER fail_online_last BEFORE INSERT ON control_audit_events WHEN NEW.category='git_profile' BEGIN SELECT RAISE(ABORT,'late audit failure'); END`)
			return err
		}))
		_, err := s.Import(ctx, raw)
		require.Error(t, err)
		for _, table := range []string{"principals", "servers", "server_identities", "resource_keys", "grants", "http_grants", "git_grants", "http_credentials", "git_credentials", "git_repositories", "configuration_import_grant_expiries"} {
			require.Zero(t, count(t, store, ctx, table), table)
		}
		require.Equal(t, before, count(t, store, ctx, "control_audit_events"))
		p, err := s.authority.GetGitRoutingProfile(ctx)
		require.NoError(t, err)
		require.Equal(t, "1", p.Revision)
		require.Empty(t, p.Origins)
		require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, `DROP TRIGGER fail_online_last`); return err }))
		_, err = s.Import(ctx, raw)
		require.NoError(t, err)
	})
}
