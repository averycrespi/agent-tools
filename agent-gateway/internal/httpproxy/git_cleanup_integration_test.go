//go:build integration

package httpproxy

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitUnfinishedUploadSettles(t *testing.T) {
	for _, mode := range []string{"early_response", "active_drain"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/topic"
			prefix := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
			entered, settled := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(settled)
				if r.Header.Get("Authorization") != "Bearer cleanup-canary" {
					t.Error("selected Git material missing")
					w.WriteHeader(403)
					return
				}
				if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
					t.Error("full duplex unavailable")
					return
				}
				got := make([]byte, len(prefix))
				_, err := io.ReadFull(r.Body, got)
				if err != nil {
					t.Error("validated prefix not forwarded")
					return
				}
				if string(got) != prefix {
					t.Error("prefix changed")
					return
				}
				close(entered)
				if mode == "early_response" {
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					_, _ = io.WriteString(w, "stop")
					_ = http.NewResponseController(w).Flush()
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
			}))
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, f.engine.Close(ctx))
				upstream.Close()
			})
			f.engine.roots = x509.NewCertPool()
			f.engine.roots.AddCert(upstream.Certificate())
			f.allow(t, upstream.URL, "allow_requests", "/repo", "")
			ctx := audit.WithSystem(t.Context())
			material, err := f.gitMaterials.Create(ctx, contract.GitCredentialDefinition{Name: "cleanup", Origin: upstream.URL, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("cleanup-canary"))
			require.NoError(t, err)
			repo, err := f.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "cleanup", URL: upstream.URL + "/repo", Aliases: []string{}, CredentialID: &material.ID})
			require.NoError(t, err)
			_, err = f.authority.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: f.credential.Principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/"},"actions":["create"]}]}`)})
			require.NoError(t, err)
			profile, err := f.authority.GetGitRoutingProfile(ctx)
			require.NoError(t, err)
			_, err = f.authority.PutGitRoutingProfile(ctx, profile.Revision, []string{upstream.URL})
			require.NoError(t, err)
			conn := f.intercept(t, upstream.URL, "http/1.1")
			require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
			u, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			_, err = fmt.Fprintf(conn, "POST /repo/git-receive-pack HTTP/1.1\r\nHost: %s\r\nContent-Type: application/x-git-receive-pack-request\r\nContent-Length: 1048576\r\nConnection: close\r\n\r\n%s", u.Host, prefix)
			require.NoError(t, err)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Git upload did not dispatch")
			}
			if mode == "early_response" {
				response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
				require.NoError(t, err)
				require.Equal(t, 413, response.StatusCode)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.Equal(t, "stop", string(body))
				require.NoError(t, response.Body.Close())
			}
			drain, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			require.NoError(t, f.engine.Close(drain))
			select {
			case <-settled:
			case <-drain.Done():
				t.Fatal("upstream Git owner did not settle")
			}
			require.NoError(t, conn.Close())
			status := f.engine.Status()
			require.Zero(t, status.Work.InUse)
			require.Zero(t, status.ActiveStreams)
			require.Zero(t, status.Connections.InUse)
			require.Eventually(t, func() bool {
				history, err := f.traffic.GitHistory(t.Context(), 0, 10)
				return err == nil && len(history.Records) == 1 && history.Records[0].Completion != nil
			}, 3*time.Second, 10*time.Millisecond)
			history, err := f.traffic.GitHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			record := history.Records[0]
			require.NotNil(t, record.Admission.Material)
			require.NotNil(t, record.Completion)
			require.Equal(t, "outcome_unknown", record.Completion.Outcome)
			require.Less(t, record.Completion.BytesSent, int64(1048576))
			require.False(t, record.Completion.TransferComplete)
			if mode == "early_response" {
				require.Equal(t, 413, record.Completion.Status)
			}
			// Material/control ownership must be available after stream/completion joins.
			_, err = f.gitMaterials.Rotate(ctx, material.ID, material.Revision, []byte("after-cleanup-canary"))
			require.NoError(t, err)
		})
	}
}
