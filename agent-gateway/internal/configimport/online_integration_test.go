//go:build integration

package configimport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configdefinition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configexport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type onlineClock struct{}

func (onlineClock) Now() time.Time { return time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC) }

var onlineTemplate = storagefixture.New("01ARZ3NDEKTSV4RRFFQ69G5FAV")

func onlineFixture(t *testing.T) (*Service, *storage.Store, context.Context) {
	t.Helper()
	ctx := audit.WithOffline(t.Context())
	owner, err := paths.Acquire(testutil.NewOwnerOnlyDataRoot(t))
	require.NoError(t, err)
	store, err := onlineTemplate.Open(ctx, owner)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()); require.NoError(t, owner.Close()) })
	authority, err := authorization.New(store, onlineClock{}, rand.Reader)
	require.NoError(t, err)
	targets, err := servers.New(store, onlineClock{}, rand.Reader)
	require.NoError(t, err)
	next := 100
	return NewOnline(store, authority, targets, onlineClock{}.Now, func() (string, error) { next++; return fmt.Sprintf("%026d", next), nil }, nil), store, ctx
}
func definition(kind, key, spec string) contract.ConfigDefinition {
	return contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: kind, Key: key, Spec: json.RawMessage(spec)}
}
func submission(t *testing.T, defs ...contract.ConfigDefinition) []byte {
	t.Helper()
	raw, err := json.Marshal(contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: defs})
	require.NoError(t, err)
	return raw
}
func onlineGraph() []contract.ConfigDefinition {
	return []contract.ConfigDefinition{
		definition("agent", "builder", `{"display_name":"Builder","state":"active","visibility":"allowed-only","http_default":"block"}`),
		definition("mcp_server", "deleted", `{"display_name":"Deleted","enabled":false,"deleted":true,"transport":null,"credentials":{"static":{},"oauth_client_secret":null,"oauth_authorization":false}}`),
		definition("mcp_grant", "expired", `{"agent":{"kind":"agent","key":"builder"},"server":{"kind":"mcp_server","key":"deleted"},"description":null,"effect":"allow","upstream_name":"tool","constraint":{"version":2,"equals":{"/integer":9007199254740993,"/decimal":1.0}},"read_only":false,"expires_at":"2020-01-01T00:00:00.000000000Z"}`),
		definition("http_credential", "http", `{"name":"HTTP","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"http_credential/http/secret"}`),
		definition("http_grant", "web", `{"agent":{"kind":"agent","key":"builder"},"description":null,"policy":{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}},"allow_private":false,"credential":{"kind":"http_credential","key":"http"}},"expires_at":"2020-01-01T00:00:00.000000000Z"}`),
		definition("git_credential", "git", `{"name":"Git","origin":"https://example.com:443","recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"git_credential/git/secret"}`),
		definition("git_repository", "repo", `{"name":"Repo","url":"https://example.com:443/org/repo","aliases":[],"credential":{"kind":"git_credential","key":"git"},"deleted":true}`),
		definition("git_grant", "read", `{"agent":{"kind":"agent","key":"builder"},"repository":{"kind":"git_repository","key":"repo"},"description":null,"policy":{"version":1,"read":true,"refs":[]},"expires_at":"2020-01-01T00:00:00.000000000Z"}`),
		definition("git_routing", "", `{"origins":[]}`),
	}
}
func count(t *testing.T, store *storage.Store, ctx context.Context, table string) int {
	t.Helper()
	n := 0
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error { return tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n) }))
	return n
}
func TestIntegrationOnlineConfigurationFullAndPerKindRoundtrip(t *testing.T) {
	source, store, ctx := onlineFixture(t)
	defs := onlineGraph()
	duplicate := defs[2]
	duplicate.Key = "duplicate"
	defs = append(defs, duplicate)
	for _, index := range []int{4, 7} {
		duplicate := defs[index]
		duplicate.Key += "-duplicate"
		defs = append(defs, duplicate)
	}
	before := count(t, store, ctx, "control_audit_events")
	raw := submission(t, defs...)
	p, err := source.Preview(ctx, raw)
	require.NoError(t, err)
	require.True(t, p.Advisory)
	require.Len(t, p.Creations, len(defs))
	require.Equal(t, before, count(t, store, ctx, "control_audit_events"))
	require.Zero(t, count(t, store, ctx, "principals"))
	result, err := source.Import(ctx, raw)
	require.NoError(t, err)
	require.Equal(t, "committed", result.Configuration)
	require.Len(t, result.Creations, len(defs))
	require.Equal(t, 2, count(t, store, ctx, "grants"))
	require.Equal(t, 2, count(t, store, ctx, "http_grants"))
	require.Equal(t, 2, count(t, store, ctx, "git_grants"))
	require.Equal(t, 6, count(t, store, ctx, "configuration_import_grant_expiries"))
	require.Equal(t, 0, count(t, store, ctx, "secret_generations"))
	require.NoError(t, source.authority.ValidateStartup(ctx, source.targets))
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var created, expiry string
		if err := tx.QueryRowContext(ctx, `SELECT created_at,expires_at FROM grants LIMIT 1`).Scan(&created, &expiry); err != nil {
			return err
		}
		actual, err := time.Parse(time.RFC3339Nano, created)
		require.NoError(t, err)
		require.Equal(t, onlineClock{}.Now(), actual)
		require.Equal(t, "2020-01-01T00:00:00.000000000Z", expiry)
		var tokens int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM principals WHERE credential_revision<>0 OR credential_id IS NOT NULL OR credential_verifier IS NOT NULL`).Scan(&tokens); err != nil {
			return err
		}
		require.Zero(t, tokens)
		return nil
	}))
	exported, err := configexport.New(store).ExportBundle(ctx)
	require.NoError(t, err)
	require.Contains(t, string(exported), "9007199254740993")
	require.Contains(t, string(exported), "1.0")
	target, targetStore, targetCtx := onlineFixture(t)
	_, err = target.Import(targetCtx, exported)
	require.NoError(t, err)
	second, err := configexport.New(targetStore).ExportBundle(targetCtx)
	require.NoError(t, err)
	require.JSONEq(t, string(exported), string(second))
	for _, d := range defs {
		_, err := source.Import(ctx, submission(t, d))
		require.Error(t, err, "identical must conflict %s", d.Kind)
	}
	// Individual files use the same graph and may bind preexisting dependencies.
	perKind, perStore, perCtx := onlineFixture(t)
	for _, d := range defs {
		single, err := json.Marshal(d)
		require.NoError(t, err)
		_, err = perKind.Import(perCtx, single)
		require.NoError(t, err, d.Kind)
	}
	all, err := configexport.New(perStore).ExportBundle(perCtx)
	require.NoError(t, err)
	require.JSONEq(t, string(exported), string(all))
}
func TestIntegrationOnlineConfigurationRollbackAndPreviewRace(t *testing.T) {
	s, store, ctx := onlineFixture(t)
	defs := onlineGraph()
	raw := submission(t, defs...)
	_, err := s.Preview(ctx, raw)
	require.NoError(t, err)
	// A real mandatory-audit failure after staged rows must roll back every owner.
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER fail_online_audit BEFORE INSERT ON control_audit_events BEGIN SELECT RAISE(ABORT,'test audit refusal'); END`)
		return err
	}))
	_, err = s.Import(ctx, raw)
	require.Error(t, err)
	for _, table := range []string{"principals", "grants", "server_identities", "http_credentials", "git_credentials", "git_repositories", "resource_keys"} {
		require.Zero(t, count(t, store, ctx, table), table)
	}
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, `DROP TRIGGER fail_online_audit`); return err }))
	_, err = s.Import(ctx, raw)
	require.NoError(t, err)
	_, err = s.Import(ctx, raw)
	require.ErrorIs(t, err, ErrConflict)
	// Two independently admitted imports never both reserve the same key.
	s2, store2, ctx2 := onlineFixture(t)
	single := submission(t, defs[0])
	_, err = s2.Preview(ctx2, single)
	require.NoError(t, err)
	var wg sync.WaitGroup
	out := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Go(func() { <-start; _, err := s2.Import(ctx2, single); out <- err })
	}
	close(start)
	wg.Wait()
	close(out)
	successes := 0
	for err := range out {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, ErrConflict)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, count(t, store2, ctx2, "principals"))
}
func TestIntegrationOnlineConfigurationRejectsDanglingAndClosedInput(t *testing.T) {
	s, store, ctx := onlineFixture(t)
	before := count(t, store, ctx, "control_audit_events")
	defs := onlineGraph()
	for _, raw := range [][]byte{[]byte(`{"format":"agent-gateway-config","version":1}`), []byte(`{"format":"agent-gateway-config","version":2,"resources":[],"unknown":"secret-canary"}`), submission(t, defs[2]), submission(t, defs[0], defs[0])} {
		_, err := s.Preview(ctx, raw)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret-canary")
		_, err = s.Import(ctx, raw)
		require.Error(t, err)
	}
	require.Zero(t, count(t, store, ctx, "principals"))
	require.Equal(t, before, count(t, store, ctx, "control_audit_events"))
	_, err := configdefinition.DecodeSubmission(submission(t, defs[0]))
	require.NoError(t, err)
}
