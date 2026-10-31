package composition

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestConfigurationImportMixedProtocolRoundTripPreservesPolicyNotAuthority(t *testing.T) {
	ctx := audit.WithSystem(t.Context())
	optionsA, cleanupA := newCompositionOptions(t)
	defer cleanupA()
	optionsA.Entropy = rand.Reader
	a, err := New(optionsA)
	require.NoError(t, err)
	defer a.shutdownConstructed()
	principal, err := a.authorization.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "Round trip", Visibility: contract.VisibilityAll})
	require.NoError(t, err)
	token, err := a.authorization.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	sentinel := filepath.Join(t.TempDir(), "activated")
	executable := filepath.Join(t.TempDir(), "server")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nprintf activated > '"+sentinel+"'\nexit 0\n"), 0700))
	server, err := a.servers.Create(ctx, servers.CreateRequest{Idempotency: compositionIdempotency("roundtrip-create"), Definition: servers.Definition{Namespace: "portable", DisplayName: "Portable", Enabled: true, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: executable, Arguments: []string{}, WorkingDirectory: filepath.Dir(executable), Environment: map[string]string{}, SecretEnvironment: map[string]string{}}}})
	require.NoError(t, err)
	_, err = a.authorization.CreateGrant(ctx, authorization.CreateGrantRequest{PrincipalID: principal.Principal.ID, Effect: contract.GrantAllow, Target: accesstarget.MCP{ServerID: server.Server.ID}}, a.servers.StoredGrantTargetExistsTx)
	require.NoError(t, err)
	_, err = a.authorization.CreateGrant(ctx, authorization.CreateGrantRequest{PrincipalID: principal.Principal.ID, Effect: contract.GrantDeny, Target: accesstarget.Tool(server.Server.ID, "blocked")}, a.servers.StoredGrantTargetExistsTx)
	require.NoError(t, err)
	credential, err := a.httpCredentials.Create(ctx, httpcredentials.Definition{Name: "HTTP", Boundary: httpcredentials.Boundary{Host: "example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("synthetic-A-http"))
	require.NoError(t, err)
	policy := contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowRequests, Request: &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: "https", Host: "example.com", Port: 443}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathPrefix, Value: "/allowed"}}, AllowPrivate: pointer(false), CredentialID: &credential.ID}
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	_, err = a.authorization.PutHTTPGrant(ctx, "", "", authorization.HTTPGrantInput{PrincipalID: principal.Principal.ID, Policy: raw})
	require.NoError(t, err)
	gitCredential, err := a.gitCredentials.Create(ctx, contract.GitCredentialDefinition{Name: "Git", Origin: "https://example.com", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("synthetic-A-git"))
	require.NoError(t, err)
	repository, err := a.authorization.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Repository", URL: "https://example.com/repo.git", Aliases: []string{"https://example.com/repo"}, CredentialID: &gitCredential.ID})
	require.NoError(t, err)
	gitRaw := json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/feature/"},"actions":["create","update"]}]}`)
	_, err = a.authorization.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: principal.Principal.ID, RepositoryID: repository.ID, Policy: gitRaw})
	require.NoError(t, err)
	_, err = a.authorization.PutGitRoutingProfile(ctx, "1", []string{"https://example.com"})
	require.NoError(t, err)
	require.NoError(t, a.httpCA.Replace(ctx, "0"))
	var caA httpca.Inspection
	require.NoError(t, optionsA.Store.View(ctx, func(tx *sql.Tx) error { var err error; caA, err = httpca.InspectTx(ctx, tx); return err }))
	exported, err := a.configuration.ExportConfiguration(ctx)
	require.NoError(t, err)
	ownerB, planB := importBytesFixture(t, exported)
	clockB := testutil.NewFakeClock(compositionTime)
	initialized, err := InitializeOwned(ctx, ownerB, planB, clockB, rand.Reader)
	require.NoError(t, err)
	require.NotEqual(t, optionsA.InstallationID, initialized.InstallationID)
	require.NotEqual(t, caA.Certificate, initialized.Certificate)
	keyA, err := os.ReadFile(filepath.Join(optionsA.Ownership.Layout().Root, "master-key"))
	require.NoError(t, err)
	keyB, err := os.ReadFile(filepath.Join(ownerB.Layout().Root, "master-key"))
	require.NoError(t, err)
	require.NotEqual(t, keyA, keyB)
	storeB, err := storage.Open(ctx, ownerB)
	require.NoError(t, err)
	defer func() { require.NoError(t, storeB.Close()) }()
	b, err := New(Options{Ownership: ownerB, Store: storeB, InstallationID: planB.Installation, CallbackURL: "http://127.0.0.1:47101/oauth/callback", Clock: clockB, Entropy: rand.Reader, Invalidate: func(contract.Invalidation) {}, Ready: func() bool { return true }})
	require.NoError(t, err)
	defer b.shutdownConstructed()
	_, err = b.authorization.Authenticate(ctx, token.Bearer)
	require.Error(t, err, "source token has no authority in B")
	agentsB, err := b.authorization.ListPrincipals(ctx, nil, contract.CollectionPageDefault)
	require.NoError(t, err)
	require.Len(t, agentsB.Items, 1)
	agentB := agentsB.Items[0]
	require.NotEqual(t, principal.Principal.ID, agentB.ID)
	require.Nil(t, agentB.Credential)
	serversB, err := b.servers.ListServers(ctx, nil, contract.CollectionPageDefault)
	require.NoError(t, err)
	require.Len(t, serversB.Items, 1)
	serverB := serversB.Items[0]
	require.True(t, serverB.ActivationRequired)
	require.Equal(t, contract.DesiredServerEnabled, serverB.DesiredState)
	require.NotEqual(t, server.Server.ID, serverB.ID)
	credentialsB, err := b.httpCredentials.List(ctx)
	require.NoError(t, err)
	require.Len(t, credentialsB, 1)
	require.False(t, credentialsB[0].Available)
	_, err = b.httpCredentials.Rotate(ctx, credentialsB[0].ID, credentialsB[0].Revision, []byte("synthetic-B-http"))
	require.NoError(t, err)
	gitCredentialsB, err := b.gitCredentials.List(ctx)
	require.NoError(t, err)
	require.Len(t, gitCredentialsB, 1)
	require.False(t, gitCredentialsB[0].Available)
	_, err = b.gitCredentials.Rotate(ctx, gitCredentialsB[0].ID, gitCredentialsB[0].Revision, []byte("synthetic-B-git"))
	require.NoError(t, err)
	reposB, err := b.authorization.ListGitRepositories(ctx)
	require.NoError(t, err)
	require.Len(t, reposB, 1)
	require.Equal(t, gitCredentialsB[0].ID, *reposB[0].CredentialID)
	newToken, err := b.authorization.IssueCredential(ctx, agentB.ID, agentB.Revision)
	require.NoError(t, err)
	lease, err := b.authorization.Authenticate(ctx, newToken.Bearer)
	require.NoError(t, err)
	lease.Release()
	for _, name := range []string{"allowed", "blocked"} {
		resultA, err := a.authorization.Evaluate(ctx, authorization.EvaluationRequest{PrincipalID: principal.Principal.ID, Target: accesstarget.Tool(server.Server.ID, name), Arguments: json.RawMessage(`{}`)})
		require.NoError(t, err)
		resultB, err := b.authorization.Evaluate(ctx, authorization.EvaluationRequest{PrincipalID: agentB.ID, Target: accesstarget.Tool(serverB.ID, name), Arguments: json.RawMessage(`{}`)})
		require.NoError(t, err)
		require.Equal(t, resultA.Decision, resultB.Decision, name)
	}
	for _, path := range []string{"/allowed/x", "/blocked"} {
		resultA, err := a.authorization.PreviewHTTPAccess(ctx, authorization.HTTPAccessInput{PrincipalID: principal.Principal.ID, URL: "https://example.com" + path, Method: "GET"})
		require.NoError(t, err)
		resultB, err := b.authorization.PreviewHTTPAccess(ctx, authorization.HTTPAccessInput{PrincipalID: agentB.ID, URL: "https://example.com" + path, Method: "GET"})
		require.NoError(t, err)
		require.Equal(t, resultA.Decision.Allowed, resultB.Decision.Allowed, path)
		require.Equal(t, resultA.Decision.Reason, resultB.Decision.Reason, path)
		if resultB.Decision.Credential != nil {
			require.Equal(t, credentialsB[0].ID, resultB.Decision.Credential.ID)
		}
	}
	for _, ref := range []string{"refs/heads/feature/x", "refs/heads/main"} {
		actions := []gitpolicy.RefAction{{Ref: ref, Action: "update"}}
		resultA, err := a.authorization.EvaluateGit(ctx, principal.Principal.ID, repository.ID, actions)
		require.NoError(t, err)
		resultB, err := b.authorization.EvaluateGit(ctx, agentB.ID, reposB[0].ID, actions)
		require.NoError(t, err)
		require.Equal(t, resultA.Allowed, resultB.Allowed, ref)
	}
	// Starting B must not activate even this credential-free imported executable.
	require.NoError(t, b.Start(ctx))
	_, err = os.Stat(sentinel)
	require.True(t, os.IsNotExist(err))
	require.Equal(t, contract.RuntimeInactive, b.manager.Status(serverB.ID).State)
	operation, err := b.servers.CreateOperation(ctx, servers.OperationRequest{ServerID: serverB.ID, Kind: contract.OperationReload, ExpectedDesiredRevision: serverB.DesiredRevision, TriggerState: pointer(b.OperationState(ctx, serverB.ID)), Idempotency: &servers.IdempotencyRequest{AuthorityID: planB.Installation, Method: "POST", Route: "/api/v2/mcp/servers/" + serverB.ID + "/operations", Key: "explicit-activation", RequestHash: [32]byte{1}, Precondition: contract.ServerETag(serverB.ID, serverB.DesiredRevision)}})
	require.NoError(t, err)
	b.manager.Trigger(serverB.ID, &operation.Operation.ID, true)
	require.Eventually(t, func() bool { _, err := os.Stat(sentinel); return err == nil }, 5*time.Second, 10*time.Millisecond)
	require.Zero(t, (<-b.Drain(ctx)).Unconfirmed)
}

func pointer[T any](value T) *T { return &value }
