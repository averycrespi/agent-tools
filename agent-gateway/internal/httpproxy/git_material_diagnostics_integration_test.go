//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitMaterialRefusalExplainsCauseWithoutDial(t *testing.T) {
	f := fixture(t)
	var connections atomic.Int64
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	upstream.StartTLS()
	defer upstream.Close()
	f.engine.roots = x509.NewCertPool()
	f.engine.roots.AddCert(upstream.Certificate())
	f.allow(t, upstream.URL, "allow_requests", "/repo", "")
	ctx := audit.WithSystem(t.Context())
	material, err := f.gitMaterials.Create(ctx, contract.GitCredentialDefinition{Name: "material-test", Origin: upstream.URL, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("actual-git-secret-canary"))
	require.NoError(t, err)
	repo, err := f.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "fixture", URL: upstream.URL + "/repo", Aliases: []string{}, CredentialID: &material.ID})
	require.NoError(t, err)
	_, err = f.authority.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: f.credential.Principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[]}`)})
	require.NoError(t, err)
	profile, err := f.authority.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	_, err = f.authority.PutGitRoutingProfile(ctx, profile.Revision, []string{upstream.URL})
	require.NoError(t, err)
	require.NoError(t, f.store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE secret_generations SET ciphertext=zeroblob(length(ciphertext)) WHERE kind='git_credential'`)
		return err
	}))
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
	f.engine.options.Diagnostics = adapter
	f.traffic.BeginDrain()
	conn := f.intercept(t, upstream.URL, "http/1.1")
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	_, err = fmt.Fprintf(conn, "GET /repo/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", u.Host)
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
	require.NoError(t, err)
	require.Equal(t, 503, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.NoError(t, conn.Close())
	require.Zero(t, connections.Load())
	require.True(t, adapter.Finish(nil))
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte(`"event":"http_proxy_rejected"`)))
	require.Contains(t, output.String(), "generation_authentication")
	require.Contains(t, output.String(), material.ID)
	require.Contains(t, output.String(), "dispatch=not_authorized")
	require.NotContains(t, output.String(), "actual-git-secret-canary")
	require.NotContains(t, output.String(), f.credential.Bearer)
}
