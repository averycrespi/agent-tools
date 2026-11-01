package composition

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestConfigurationImportPreservesExpiredAndDeletedPolicyWithoutSourceHistory(t *testing.T) {
	expired := "2020-01-01T00:00:00.000000000Z"
	value := contract.ConfigExport{Format: contract.ConfigExportFormat, Version: 1, Agents: []contract.ConfigAgent{{Ref: "agent/1", DisplayName: "Expired policy", State: contract.PrincipalActive, Visibility: contract.VisibilityAll, HTTPDefault: contract.HTTPDefaultBlock}}, MCP: contract.ConfigMCP{Servers: []contract.ConfigServer{{Ref: "mcp/deleted", Namespace: "deleted", DisplayName: "Deleted", Deleted: true, Transport: json.RawMessage("null"), Credentials: contract.ConfigServerCredentials{Static: map[string]string{}}}}, Grants: []contract.ConfigMCPGrant{{Agent: "agent/1", Server: "mcp/deleted", Effect: contract.GrantAllow, UpstreamName: pointer("tool"), Constraint: json.RawMessage(`{"version":2,"equals":{"/x":9007199254740993,"/decimal":1.0}}`), ExpiresAt: &expired}}}, HTTP: contract.ConfigHTTP{Credentials: []contract.ConfigHTTPCredential{}, Grants: []contract.ConfigHTTPGrant{{Agent: "agent/1", Policy: contract.ConfigHTTPPolicy{Version: 1, Type: contract.HTTPAllowRequests, Request: &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: "https", Host: "example.com", Port: 443}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}, AllowPrivate: pointer(false)}, ExpiresAt: &expired}}}, Git: contract.ConfigGit{Credentials: []contract.ConfigGitCredential{}, Repositories: []contract.ConfigGitRepository{{Ref: "repository/1", Name: "Deleted repository", URL: "https://example.com:443/repo", Aliases: []string{}, Deleted: true}}, Grants: []contract.ConfigGitGrant{{Agent: "agent/1", Repository: "repository/1", Policy: contract.GitPolicy{Version: 1, Read: true, Refs: []contract.GitRefRule{}}, ExpiresAt: &expired}}, RoutingOrigins: []string{}}}
	data, err := json.Marshal(value)
	require.NoError(t, err)
	owner, plan := importBytesFixture(t, data)
	clock := testutil.NewFakeClock(compositionTime)
	_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
	require.NoError(t, err)
	store, err := storage.Open(t.Context(), owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	built, err := New(Options{Ownership: owner, Store: store, InstallationID: plan.Installation, CallbackURL: "http://127.0.0.1:47102/oauth/callback", Clock: clock, Entropy: rand.Reader, Invalidate: func(contract.Invalidation) {}, Ready: func() bool { return true }})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		var count int
		require.NoError(t, tx.QueryRow(`SELECT count(*) FROM configuration_import_grant_expiries`).Scan(&count))
		require.Equal(t, 3, count)
		for _, table := range []string{"grants", "http_grants", "git_grants"} {
			var created, expiry string
			require.NoError(t, tx.QueryRow("SELECT created_at,expires_at FROM "+table).Scan(&created, &expiry))
			require.Equal(t, compositionTime.UTC().Format(contract.AuditTimestampLayout), created)
			require.Equal(t, expired, expiry)
		}
		var constraint string
		require.NoError(t, tx.QueryRow(`SELECT constraint_json FROM grants`).Scan(&constraint))
		require.Contains(t, constraint, "9007199254740993")
		require.Contains(t, constraint, "1.0")
		return nil
	}))
	agents, err := built.authorization.ListPrincipals(t.Context(), nil, contract.CollectionPageDefault)
	require.NoError(t, err)
	repos, err := built.authorization.ListGitRepositories(t.Context())
	require.NoError(t, err)
	require.Empty(t, repos, "deleted retained identities are not active repositories")
	preview, err := built.authorization.PreviewHTTPAccess(t.Context(), authorization.HTTPAccessInput{PrincipalID: agents.Items[0].ID, URL: "https://example.com/x", Method: "GET"})
	require.NoError(t, err)
	require.False(t, preview.Decision.Allowed)
	var repoID string
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT id FROM git_repositories`).Scan(&repoID) }))
	decision, err := built.authorization.EvaluateGit(t.Context(), agents.Items[0].ID, repoID, []gitpolicy.RefAction{})
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	// The import exception cannot bless an unrelated/corrupted expiry.
	require.NoError(t, store.Mutate(audit.WithOffline(t.Context()), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE http_grants SET expires_at='2019-01-01T00:00:00.000000000Z'`)
		return err
	}))
	require.Error(t, built.authorization.ValidateStartup(t.Context(), built.servers))
	require.NoError(t, store.Mutate(audit.WithOffline(t.Context()), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE http_grants SET expires_at=?`, expired)
		return err
	}))
	require.NoError(t, built.authorization.ValidateStartup(t.Context(), built.servers))
	require.NoError(t, store.Mutate(audit.WithOffline(t.Context()), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM configuration_import_grant_expiries WHERE kind='git'`)
		return err
	}))
	require.Error(t, built.authorization.ValidateStartup(t.Context(), built.servers), "current-schema validation must require the exact imported expiry receipt")
}
