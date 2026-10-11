package invocation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func invocationGitRequest(t *testing.T, authority *authorization.Repository, principal string, allow bool, wire string) *gitwire.Request {
	t.Helper()
	repo, err := authority.PutGitRepository(t.Context(), "", "", contract.GitRepositoryDefinition{Name: "repo", URL: "https://example.com/repo", Aliases: []string{}})
	require.NoError(t, err)
	profile, err := authority.GetGitRoutingProfile(t.Context())
	require.NoError(t, err)
	profile, err = authority.PutGitRoutingProfile(t.Context(), profile.Revision, []string{"https://example.com"})
	require.NoError(t, err)
	if allow {
		_, err = authority.PutGitGrant(t.Context(), "", "", authorization.GitGrantInput{PrincipalID: principal, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/"},"actions":["create"]}]}`)})
		require.NoError(t, err)
	}
	target, err := httppolicy.ParseRequest(repo.URL+"/git-receive-pack", "POST", "example.com", "", nil)
	require.NoError(t, err)
	request, err := gitwire.New(t.Context(), target, repo, profile.Revision, http.Header{"Content-Type": {"application/x-git-receive-pack-request"}}, io.NopCloser(strings.NewReader(wire)))
	require.NoError(t, err)
	return request
}

func TestGitOnlyBoundedRefsEnterTrafficAndBackup(t *testing.T) {
	c, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	ref, oid, pack := "refs/heads/observed-private-canary", "1234567890abcdef1234567890abcdef12345678", "PACKopaque-private-canary"
	line := strings.Repeat("0", 40) + " " + oid + " " + ref
	wire := fmt.Sprintf("%04x%s0000%s", len(line)+4, line, pack)
	request := invocationGitRequest(t, authority, principal.ID, true, wire)
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	identity, err := audits.PrepareIdentity()
	require.NoError(t, err)
	result, err := c.AdmitGit(t.Context(), lease, identity, request, publicHTTPFacts(), nil)
	require.NoError(t, err)
	require.True(t, result.DispatchAuthorized)
	body, err := request.Dispatch()
	require.NoError(t, err)
	forwarded, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, wire, string(forwarded))
	require.NoError(t, body.Close())
	_, err = request.Dispatch()
	require.Error(t, err, "exact parsed body remains one use")
	result.Settle()
	require.NoError(t, c.CompleteGit(t.Context(), result, gitTrafficCompletion()))
	traffic := audits.traffic
	waitTraffic(t, traffic)
	require.NoError(t, audits.store.SelectTraffic(t.Context(), "", invocationID(90)))
	root := t.TempDir()
	backup := filepath.Join(root, "traffic.db")
	traffic.writerGate.Lock()
	busy := traffic.BackupPair(t.Context(), audits.store, filepath.Join(root, "control.db"), backup)
	traffic.writerGate.Unlock()
	require.ErrorIs(t, busy, ErrTrafficCapacity)
	require.NoError(t, traffic.BackupPair(t.Context(), audits.store, filepath.Join(root, "control.db"), backup))
	for _, path := range []string{traffic.path, traffic.path + "-wal", backup} {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		for _, canary := range []string{oid, pack, credential.Bearer} {
			require.NotContains(t, string(raw), canary)
		}
	}
	history, err := traffic.GitHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.Equal(t, &contract.GitTrafficRefEvidence{State: "complete", Refs: []contract.GitTrafficRequestedRef{{Name: ref, Action: "create"}}}, history.Records[0].Admission.RefEvidence)
	retained, err := os.ReadFile(backup)
	require.NoError(t, err)
	require.Contains(t, string(retained), ref)
}

func TestGitOptionalRecordingNeverGrantsOrBlocksExecution(t *testing.T) {
	for _, mode := range []string{"stalled", "full", "unavailable", "uncertain", "invalid capture", "deny"} {
		t.Run(mode, func(t *testing.T) {
			c, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
			request := invocationGitRequest(t, authority, principal.ID, mode != "deny", "0000")
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var once sync.Once
			traffic, _ := trafficFixture(t, func(config *TrafficConfig) {
				if mode == "full" {
					config.QueueRecords = 1
					config.BatchRecords = 1
				}
			}, func(point string) error {
				if point == "before_begin" {
					once.Do(func() { close(entered); <-release })
				}
				if point == "acknowledgment" && mode == "uncertain" {
					return errors.New("uncertain history")
				}
				return nil
			})
			audits.traffic = traffic
			if mode == "unavailable" || mode == "deny" {
				traffic.BeginDrain()
			}
			if mode == "full" {
				require.NotNil(t, traffic.ObserveMCP(trafficPrepared(77)))
				<-entered
			}
			lease, err := authority.Authenticate(t.Context(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			if mode == "invalid capture" {
				identity = PreparedAdmission{}
			}
			type outcome struct {
				result GitAdmissionResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				r, e := c.AdmitGit(t.Context(), lease, identity, request, publicHTTPFacts(), nil)
				done <- outcome{r, e}
			}()
			var got outcome
			select {
			case got = <-done:
			case <-time.After(time.Second):
				t.Fatal("Git admission waited for history")
			}
			require.NoError(t, got.err)
			require.Equal(t, mode != "deny", got.result.DispatchAuthorized)
			if got.result.DispatchAuthorized {
				body, err := request.Dispatch()
				require.NoError(t, err)
				raw, err := io.ReadAll(body)
				require.NoError(t, err)
				require.Equal(t, "0000", string(raw))
				require.NoError(t, body.Close())
				_, err = request.Dispatch()
				require.Error(t, err)
				got.result.Settle()
				got.result.Settle()
				completion := gitTrafficCompletion()
				completion.Outcome = "nonmutation"
				terminal := make(chan error, 1)
				go func() { terminal <- c.CompleteGit(t.Context(), got.result, completion) }()
				select {
				case <-terminal:
				case <-time.After(time.Second):
					t.Fatal("Git completion waited for history")
				}
			}
			unblock()
			waitTraffic(t, traffic)
			if mode == "uncertain" {
				require.False(t, traffic.Healthy())
			}
		})
	}
}
