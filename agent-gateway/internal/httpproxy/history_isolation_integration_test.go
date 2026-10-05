//go:build integration

package httpproxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

// These faults affect only disposable optional history. The WAL case is a
// pinned-reader capacity refusal, not physical disk exhaustion or power loss.
func isolateProxyHistory(t *testing.T, f *proxyFixture, mode string) {
	t.Helper()
	switch mode {
	case "absent", "stalled-open":
		facade := invocation.NewOptionalTraffic(invocation.DefaultTrafficConfig())
		t.Cleanup(func() { require.NoError(t, facade.Close()) })
		if mode == "stalled-open" {
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			t.Cleanup(unblock)
			facade.StartOpening(func() (*invocation.TrafficStore, error) {
				close(entered)
				<-release
				return nil, errors.New("injected unavailable history")
			})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("history opener did not enter")
			}
		}
		evidence, err := invocation.NewTrafficRepository(facade, fixtureClock{}, rand.Reader, func(contract.Invalidation) {})
		require.NoError(t, err)
		admissions, err := invocation.NewAdmissionCoordinator(evidence, f.authority)
		require.NoError(t, err)
		f.engine.options.Evidence, f.engine.options.Admissions = evidence, admissions
		f.traffic = facade
	case "faulted":
		// Unsafe permissions fault recording on its next reservation, without
		// modifying authority or replacing the outstanding history owner.
		require.NoError(t, os.Chmod(f.trafficPath, 0o644))
	case "full":
		db, err := sql.Open("sqlite3", "file:"+f.trafficPath+"?mode=rw")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		pinContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		reader, err := db.BeginTx(pinContext, &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		t.Cleanup(func() {
			err := reader.Rollback()
			if !errors.Is(err, sql.ErrTxDone) {
				require.NoError(t, err)
			}
		})
		var generation string
		require.NoError(t, reader.QueryRowContext(t.Context(), "SELECT generation FROM traffic_meta").Scan(&generation))
		// A separate connection grows the WAL while the first pins its old snapshot.
		writer, err := db.Conn(t.Context())
		require.NoError(t, err)
		defer func() { require.NoError(t, writer.Close()) }()
		_, err = writer.ExecContext(t.Context(), "PRAGMA wal_autocheckpoint=0")
		require.NoError(t, err)
		for range 100 {
			_, err = writer.ExecContext(t.Context(), "UPDATE traffic_meta SET pruning=pruning+1")
			require.NoError(t, err)
		}
		info, err := os.Stat(f.trafficPath + "-wal")
		require.NoError(t, err)
		require.Greater(t, info.Size(), int64(400<<10))
	}
}

func TestIntegrationProxyOutcomesIndependentOfHistory(t *testing.T) {
	for _, mode := range []string{"healthy", "absent", "faulted", "full", "stalled-open"} {
		t.Run(mode, func(t *testing.T) {
			// Each fault retains its own installation/writer. Serial protocols reuse
			// only setup, after live owners settle; all 25 comparisons still run.
			f := fixtureWithTrafficConfig(t, nil, nil, func(c *invocation.TrafficConfig) { c.BudgetBytes = 1 << 20 })
			isolateProxyHistory(t, f, mode)
			for _, protocol := range []string{"http", "connect", "http/1.1", "h2", "git"} {
				t.Run(protocol, func(t *testing.T) { qualifyProxyHistory(t, f, protocol, mode) })
			}
		})
	}
}

func qualifyProxyHistory(t *testing.T, f *proxyFixture, protocol, mode string) {
	t.Helper()
	discardedBefore := f.traffic.Status(t.Context()).Delivery.Discarded
	const bodyCanary = "private-request-body-canary"
	const reply = "upstream-response-canary"
	var calls atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != bodyCanary {
			t.Error("upstream request identity changed")
		}
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credential reached upstream")
		}
		w.Header().Set("Proxy-Status", "AgentGateway; error=spoofed")
		w.Header().Set(contract.HTTPProxyCorrelationHeader, "spoofed")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, reply)
	}))
	if protocol == "http" || protocol == "connect" {
		upstream.Start()
	} else {
		upstream.StartTLS()
		f.engine.roots = x509.NewCertPool()
		f.engine.roots.AddCert(upstream.Certificate())
	}
	t.Cleanup(upstream.Close)
	f.allow(t, upstream.URL, "allow_requests", "", "")
	if protocol == "connect" {
		f.allow(t, upstream.URL, "allow_tunnel", "", "")
	}
	path := "/resource"
	if protocol == "git" {
		ctx := audit.WithSystem(t.Context())
		repo, err := f.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "isolation", URL: upstream.URL + "/repo", Aliases: []string{}})
		require.NoError(t, err)
		_, err = f.authority.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: f.credential.Principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[]}`)})
		require.NoError(t, err)
		profile, err := f.authority.GetGitRoutingProfile(ctx)
		require.NoError(t, err)
		_, err = f.authority.PutGitRoutingProfile(ctx, profile.Revision, []string{upstream.URL})
		require.NoError(t, err)
		path = "/repo/git-upload-pack"
	}
	// A second independently authorized request proves a previously faulted
	// recorder cannot change the next live outcome or cause a hidden replay.
	for index := range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		request, err := http.NewRequestWithContext(ctx, "POST", upstream.URL+path, strings.NewReader(bodyCanary))
		require.NoError(t, err)
		if protocol == "git" {
			request.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		}
		var response *http.Response
		var closeConn func()
		if protocol == "http" {
			request.Header.Set("Proxy-Authorization", "Bearer "+f.credential.Bearer)
			response, err = f.client(t).Do(request)
			closeConn = func() {}
		} else if protocol == "connect" {
			conn, dialErr := net.DialTimeout("tcp", f.address, time.Second)
			require.NoError(t, dialErr)
			t.Cleanup(func() { _ = conn.Close() })
			require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
			_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", request.URL.Host, request.URL.Host, f.credential.Bearer)
			require.NoError(t, err)
			reader := bufio.NewReader(conn)
			connected, readErr := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
			require.NoError(t, readErr)
			require.Equal(t, http.StatusOK, connected.StatusCode)
			require.NoError(t, request.Write(conn))
			response, err = http.ReadResponse(reader, request)
			closeConn = func() { require.NoError(t, conn.Close()) }
		} else {
			alpn := "http/1.1"
			if protocol == "h2" {
				alpn = "h2"
			}
			conn := f.intercept(t, upstream.URL, alpn)
			require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
			closeConn = func() { require.NoError(t, conn.Close()) }
			if protocol == "h2" {
				client, clientErr := (&http2.Transport{}).NewClientConn(conn)
				require.NoError(t, clientErr)
				response, err = client.RoundTrip(request)
			} else {
				require.NoError(t, request.Write(conn))
				response, err = http.ReadResponse(bufio.NewReader(conn), request)
			}
		}
		require.NoError(t, err)
		got, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusCreated, response.StatusCode)
		require.Equal(t, reply, string(got))
		if protocol == "connect" {
			// Opaque bytes are not interpreted or rewritten by Gateway.
			require.Equal(t, "AgentGateway; error=spoofed", response.Header.Get("Proxy-Status"))
		} else {
			require.Empty(t, response.Header.Get("Proxy-Status"))
			require.Empty(t, response.Header.Get(contract.HTTPProxyCorrelationHeader))
		}
		closeConn()
		cancel()
		require.EqualValues(t, index+1, calls.Load())
		require.Eventually(t, func() bool {
			s := f.engine.Status()
			return s.Work.InUse == 0 && s.ActiveStreams == 0 && s.ActiveTunnels == 0 && s.Connections.InUse == 0
		}, 5*time.Second, time.Millisecond)
		if mode == "faulted" {
			require.Eventually(t, func() bool { return f.traffic.Status(t.Context()).Faulted }, time.Second, time.Millisecond)
		}
	}
	status := f.traffic.Status(t.Context())
	switch mode {
	case "absent":
		require.Equal(t, "disabled", status.State)
	case "stalled-open":
		require.Equal(t, "opening", status.State)
	case "full":
		require.Eventually(t, func() bool {
			health := f.traffic.Status(t.Context())
			return health.Delivery.Discarded > discardedBefore && health.Delivery.QueueRecords == 0
		}, 2*time.Second, time.Millisecond)
		// Status reads can themselves occupy the read gate. Both reasons are
		// bounded capacity refusals while the independently pinned WAL survives.
		require.Contains(t, []string{"checkpoint_reader", "checkpoint_unavailable"}, f.traffic.Status(t.Context()).PressureReason)
		require.True(t, f.traffic.Healthy())
	}
	if mode != "healthy" {
		require.Greater(t, f.traffic.Status(t.Context()).Delivery.Discarded, discardedBefore)
	}
	snapshot, err := json.Marshal(f.engine.options.Observations.Status())
	require.NoError(t, err)
	require.NotContains(t, string(snapshot), bodyCanary)
	require.NotContains(t, string(snapshot), reply)
	require.NotContains(t, string(snapshot), f.credential.Bearer)
	require.EqualValues(t, 2, calls.Load(), fmt.Sprintf("%s/%s dispatched twice only", protocol, mode))
}
