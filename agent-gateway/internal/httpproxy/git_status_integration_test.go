//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
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

func TestIntegrationGitStatusObservationPreservesLiveResponse(t *testing.T) {
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	full := pkt("unpack ok\n") + pkt("ok refs/heads/private-a\n") + pkt("ng refs/heads/private-b secret-message-canary\n") + "0000"
	for _, mode := range []string{"partial", "success", "failure", "truncated", "missing", "gzip", "unsupported_encoding", "sideband", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			wire := []byte(full)
			want := "reported_partial"
			encoding := ""
			switch mode {
			case "success":
				wire = []byte(pkt("unpack ok\n") + pkt("ok refs/heads/private-a\n") + pkt("ok refs/heads/private-b\n") + "0000")
				want = "reported_success"
			case "failure":
				wire = []byte(pkt("unpack ok\n") + pkt("ng refs/heads/private-a rejected\n") + pkt("ng refs/heads/private-b rejected\n") + "0000")
				want = "reported_failure"
			case "truncated":
				wire = wire[:len(wire)-2]
				want = ""
			case "missing":
				wire = nil
				want = ""
			case "gzip":
				var b bytes.Buffer
				z := gzip.NewWriter(&b)
				_, err := z.Write(wire)
				require.NoError(t, err)
				require.NoError(t, z.Close())
				wire = b.Bytes()
				encoding = "gzip"
				want = ""
			case "unsupported_encoding":
				encoding = "br"
				want = ""
			case "sideband":
				wire = []byte(pkt("\x02secret progress") + pkt("\x01"+full) + "0000")
			case "interrupted":
				want = ""
			}
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("observed push did not request identity")
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
				if encoding != "" {
					w.Header().Set("Content-Encoding", encoding)
				}
				if mode == "interrupted" {
					w.Header().Set("Content-Length", fmt.Sprint(len(wire)+10))
				}
				_, _ = w.Write(wire)
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
			repo, err := f.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Original name", URL: upstream.URL + "/repo", Aliases: []string{}})
			require.NoError(t, err)
			grant, err := f.authority.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: f.credential.Principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/"},"actions":["delete"]}]}`)})
			require.NoError(t, err)
			profile, err := f.authority.GetGitRoutingProfile(ctx)
			require.NoError(t, err)
			_, err = f.authority.PutGitRoutingProfile(ctx, profile.Revision, []string{upstream.URL})
			require.NoError(t, err)
			caps := "report-status"
			if mode == "sideband" {
				caps += " side-band-64k"
			}
			commands := pkt(strings.Repeat("1", 40)+" "+strings.Repeat("0", 40)+" refs/heads/private-a\x00"+caps) + pkt(strings.Repeat("1", 40)+" "+strings.Repeat("0", 40)+" refs/heads/private-b") + "0000"
			conn := f.intercept(t, upstream.URL, "http/1.1")
			require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
			u, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			_, err = fmt.Fprintf(conn, "POST /repo/git-receive-pack HTTP/1.1\r\nHost: %s\r\nContent-Type: application/x-git-receive-pack-request\r\nAccept-Encoding: gzip\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", u.Host, len(commands), commands)
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
			require.NoError(t, err)
			require.Equal(t, 200, response.StatusCode)
			got, readErr := io.ReadAll(response.Body)
			if mode == "interrupted" {
				require.Error(t, readErr)
			} else {
				require.NoError(t, readErr)
				require.Equal(t, string(wire), string(got))
			}
			require.Equal(t, encoding, response.Header.Get("Content-Encoding"))
			require.NoError(t, response.Body.Close())
			require.NoError(t, conn.Close())
			drain, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			require.NoError(t, f.engine.Close(drain))
			history, err := f.traffic.GitHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			r := history.Records[0]
			require.NotNil(t, r.Completion)
			require.Equal(t, want, r.Completion.ReportedResult)
			require.Equal(t, "outcome_unknown", r.Completion.Outcome)
			require.Equal(t, mode != "interrupted", r.Completion.TransferComplete)
			require.NoError(t, f.authority.DeleteGitGrant(ctx, grant.ID, grant.Revision))
			repo.Name = "Renamed"
			repo.CredentialID = nil
			_, err = f.authority.PutGitRepository(ctx, repo.ID, repo.Revision, repo.GitRepositoryDefinition)
			require.NoError(t, err)
			retained, err := f.traffic.GitHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Equal(t, r, retained.Records[0])
			require.Equal(t, "Original name", r.Admission.Policy.RepositoryName)
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-a")
			require.NotContains(t, string(raw), "private-b")
			require.NotContains(t, string(raw), "secret-message")
			require.NotContains(t, string(raw), strings.Repeat("1", 40))
			httpHistory, err := f.traffic.HTTPHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			for _, record := range httpHistory.Records {
				require.NotNil(t, record.Admission.Target)
				require.Empty(t, record.Admission.Target.Scheme, "only enclosing CONNECT traffic may coexist with the Git record")
				require.Empty(t, record.Admission.Target.Method, "Git must not duplicate ordinary HTTP history")
			}
		})
	}
}
