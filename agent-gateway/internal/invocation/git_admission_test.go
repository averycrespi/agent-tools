package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
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

func TestGitWireMaterialNeverEntersTrafficOrBackup(t *testing.T) {
	coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	repo, err := authority.PutGitRepository(t.Context(), "", "", contract.GitRepositoryDefinition{Name: "repo", URL: "https://example.com/repo", Aliases: []string{}})
	require.NoError(t, err)
	profile, err := authority.GetGitRoutingProfile(t.Context())
	require.NoError(t, err)
	profile, err = authority.PutGitRoutingProfile(t.Context(), profile.Revision, []string{"https://example.com"})
	require.NoError(t, err)
	_, err = authority.PutGitGrant(t.Context(), "", "", authorization.GitGrantInput{PrincipalID: principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/"},"actions":["create"]}]}`)})
	require.NoError(t, err)
	ref, oid, pack := "refs/heads/observed-private-canary", "1234567890abcdef1234567890abcdef12345678", "PACKopaque-private-canary"
	line := strings.Repeat("0", 40) + " " + oid + " " + ref
	wire := fmt.Sprintf("%04x%s0000%s", len(line)+4, line, pack)
	target, err := httppolicy.ParseRequest(repo.URL+"/git-receive-pack", "POST", "example.com", "", nil)
	require.NoError(t, err)
	request, err := gitwire.New(t.Context(), target, repo, profile.Revision, http.Header{"Content-Type": {"application/x-git-receive-pack-request"}}, io.NopCloser(strings.NewReader(wire)))
	require.NoError(t, err)
	traffic, _ := trafficFixture(t, nil, nil)
	audits.traffic = traffic
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	identity, err := audits.PrepareIdentity()
	require.NoError(t, err)
	result, err := coordinator.AdmitGit(t.Context(), lease, identity, request, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, nil)
	require.NoError(t, err)
	require.True(t, result.DispatchAuthorized)
	body, err := request.Dispatch()
	require.NoError(t, err)
	forwarded, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, wire, string(forwarded))
	require.NoError(t, body.Close())
	require.NoError(t, coordinator.CompleteGit(t.Context(), result, gitTrafficCompletion()))
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
		for _, canary := range []string{ref, oid, pack, credential.Bearer} {
			require.NotContains(t, string(raw), canary)
		}
	}
}

func TestGitReceiptConfirmationRaces(t *testing.T) {
	for _, mode := range []string{"allow", "policy", "repository", "cancel", "drain", "lost acknowledgment", "deny"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
			repo, err := authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "repo", URL: "https://example.com/repo", Aliases: []string{}})
			require.NoError(t, err)
			profile, err := authority.GetGitRoutingProfile(ctx)
			require.NoError(t, err)
			profile, err = authority.PutGitRoutingProfile(ctx, profile.Revision, []string{"https://example.com"})
			require.NoError(t, err)
			var grant contract.GitGrant
			if mode != "deny" {
				grant, err = authority.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/"},"actions":["create"]}]}`)})
				require.NoError(t, err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			traffic, _ := trafficFixture(t, nil, func(point string) error {
				if point == "before_begin" {
					once.Do(func() {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
				if point == "acknowledgment" && mode == "lost acknowledgment" {
					return errors.New("uncertain")
				}
				return nil
			})
			audits.traffic = traffic
			lease, err := authority.Authenticate(ctx, credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			target, err := httppolicy.ParseRequest(repo.URL+"/git-receive-pack", "POST", "example.com", "", nil)
			require.NoError(t, err)
			request, err := gitwire.New(ctx, target, repo, profile.Revision, http.Header{"Content-Type": {"application/x-git-receive-pack-request"}}, io.NopCloser(strings.NewReader("0000")))
			require.NoError(t, err)
			facts := httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
			type outcome struct {
				result GitAdmissionResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := coordinator.AdmitGit(ctx, lease, identity, request, facts, nil)
				done <- outcome{result, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("admission never reached writer")
			}
			switch mode {
			case "policy":
				require.NoError(t, authority.DeleteGitGrant(ctx, grant.ID, grant.Revision))
			case "repository":
				def := repo.GitRepositoryDefinition
				def.Name = "edited"
				_, err = authority.PutGitRepository(ctx, repo.ID, repo.Revision, def)
				require.NoError(t, err)
			case "cancel":
				cancel()
			case "drain":
				traffic.BeginDrain()
			}
			unblock()
			out := <-done
			if mode == "allow" {
				require.NoError(t, out.err)
				require.True(t, out.result.DispatchAuthorized)
				completion := gitTrafficCompletion()
				completion.Outcome = "nonmutation"
				require.NoError(t, coordinator.CompleteGit(t.Context(), out.result, completion))
			} else {
				require.False(t, out.result.DispatchAuthorized)
			}
			if mode == "lost acknowledgment" {
				require.False(t, out.result.Committed)
				require.Error(t, out.err)
			}
			traffic.mu.Lock()
			require.Empty(t, traffic.pins)
			traffic.mu.Unlock()
			history, err := traffic.GitHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			if out.result.Committed || mode == "lost acknowledgment" {
				require.Len(t, history.Records, 1)
			}
		})
	}
}
