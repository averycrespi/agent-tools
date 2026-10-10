//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
)

type addressFixtureResolver struct {
	answers []netip.Addr
	calls   atomic.Int64
}

func (r *addressFixtureResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls.Add(1)
	return r.answers, nil
}

func installAddressCandidates(f *proxyFixture, addresses ...string) (*addressFixtureResolver, *atomic.Int64) {
	resolver := &addressFixtureResolver{}
	for _, address := range addresses {
		resolver.answers = append(resolver.answers, netip.MustParseAddr(address))
	}
	dials := new(atomic.Int64)
	dialer := &net.Dialer{}
	f.engine.options.Remote = remote.New(remote.Options{Resolver: resolver, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return dialer.DialContext(ctx, network, address)
	}})
	return resolver, dials
}

func allowNamedAddress(t *testing.T, f *proxyFixture, raw, kind, credential string) {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	require.NoError(t, err)
	private := true
	policy := contract.HTTPPolicy{Version: 1, Type: contract.HTTPGrantType(kind), AllowPrivate: &private}
	if kind == "allow_tunnel" {
		policy.Destination = &contract.HTTPDestinationSelector{Host: u.Hostname(), Port: uint16(port)}
	} else {
		policy.Request = &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: u.Scheme, Host: u.Hostname(), Port: uint16(port)}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}
		if credential != "" {
			policy.CredentialID = &credential
		}
	}
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	_, err = f.authority.PutHTTPGrant(audit.WithSystem(t.Context()), "", "", authorization.HTTPGrantInput{PrincipalID: f.credential.Principal.ID, Policy: encoded})
	require.NoError(t, err)
}

func TestIntegrationAddressSelectionHTTPWire(t *testing.T) {
	for _, mode := range []string{"fallback", "all-fail", "mixed-blocked", "post-write"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			var calls atomic.Int64
			payload := "one-logical-upload-canary"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if string(body) != payload || r.Header.Get("Proxy-Authorization") != "" {
					t.Error("bad payload or leaked proxy credential")
				}
				if mode == "post-write" {
					conn, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				_, _ = w.Write(body)
			}))
			defer upstream.Close()
			origin := "http://" + net.JoinHostPort("approved.example", strconv.Itoa(upstream.Listener.Addr().(*net.TCPAddr).Port))
			allowNamedAddress(t, f, origin, "allow_requests", "")
			// Unconfigured 127/8 aliases can stall on Darwin. The IPv4-only
			// fixture has no IPv6 listener, so use configured loopback refusal.
			answers := []string{"::1", "127.0.0.1", "127.0.0.3"}
			if mode == "mixed-blocked" {
				answers = append(answers, "169.254.169.254")
			}
			if mode == "all-fail" {
				upstream.Close()
				answers = []string{"::1", "127.0.0.1"}
			}
			resolver, dials := installAddressCandidates(f, answers...)
			response := f.request(t, f.client(t), "POST", origin+"/upload", strings.NewReader(payload))
			if mode == "fallback" {
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.Equal(t, payload, string(body))
				require.Equal(t, 200, response.StatusCode)
				require.NoError(t, response.Body.Close())
			} else {
				status := 502
				if mode == "mixed-blocked" {
					status = 403
				}
				assertFailureResponse(t, response, status)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			require.NoError(t, f.engine.Wait(ctx))
			require.EqualValues(t, 1, resolver.calls.Load())
			if mode == "mixed-blocked" {
				require.Zero(t, dials.Load())
			} else {
				require.EqualValues(t, 2, dials.Load())
			}
			if mode == "fallback" || mode == "post-write" {
				require.EqualValues(t, 1, calls.Load())
			} else {
				require.Zero(t, calls.Load())
			}
			observed := f.engine.options.Observations.Status().Protocols[diagnostics.HTTP]
			require.EqualValues(t, 1, observed.Requests)
			if mode != "mixed-blocked" {
				require.EqualValues(t, 1, observed.Executions, "TCP attempts are not logical executions")
			}
		})
	}
}

func TestIntegrationAddressSelectionOpaqueConnectWire(t *testing.T) {
	f := fixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	payload := "opaque-single-dispatch"
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			done <- err
			return
		}
		body := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, body); err != nil {
			done <- err
			return
		}
		if string(body) != payload {
			done <- fmt.Errorf("unexpected tunnel bytes")
			return
		}
		_, err = conn.Write(body)
		done <- err
	}()
	authority := net.JoinHostPort("approved.example", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	allowNamedAddress(t, f, "http://"+authority, "allow_tunnel", "")
	resolver, dials := installAddressCandidates(f, "::1", "127.0.0.1")
	conn, err := net.DialTimeout("tcp", f.address, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", authority, authority, f.credential.Bearer)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	_, err = io.WriteString(conn, payload)
	require.NoError(t, err)
	body := make([]byte, len(payload))
	_, err = io.ReadFull(reader, body)
	require.NoError(t, err)
	require.Equal(t, payload, string(body))
	require.NoError(t, conn.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(4 * time.Second):
		t.Fatal("tunnel fixture did not join")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, f.engine.Wait(ctx))
	require.EqualValues(t, 1, resolver.calls.Load())
	require.EqualValues(t, 2, dials.Load())
	observed := f.engine.options.Observations.Status().Protocols[diagnostics.Connect]
	require.EqualValues(t, 1, observed.Requests)
	require.EqualValues(t, 1, observed.Executions)
}

func TestIntegrationAddressSelectionGitPostWriteWire(t *testing.T) {
	f := fixture(t)
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	commands := pkt(strings.Repeat("1", 40)+" "+strings.Repeat("0", 40)+" refs/heads/private\x00report-status") + "0000"
	var calls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != commands || r.Header.Get("Authorization") != "Bearer selected-git-canary" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("Git dispatch lost bytes/material or disclosed proxy credential")
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()
	origin := "https://" + net.JoinHostPort(upstream.Certificate().DNSNames[0], strconv.Itoa(upstream.Listener.Addr().(*net.TCPAddr).Port))
	f.engine.roots = x509.NewCertPool()
	f.engine.roots.AddCert(upstream.Certificate())
	allowNamedAddress(t, f, origin, "allow_requests", "")
	ctx := audit.WithSystem(t.Context())
	material, err := f.gitMaterials.Create(ctx, contract.GitCredentialDefinition{Name: "selected", Origin: origin, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("selected-git-canary"))
	require.NoError(t, err)
	repo, err := f.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "fixture", URL: origin + "/repo", Aliases: []string{}, CredentialID: &material.ID})
	require.NoError(t, err)
	_, err = f.authority.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: f.credential.Principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/"},"actions":["delete"]}]}`)})
	require.NoError(t, err)
	profile, err := f.authority.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	_, err = f.authority.PutGitRoutingProfile(ctx, profile.Revision, []string{origin})
	require.NoError(t, err)
	_, dials := installAddressCandidates(f, "::1", "127.0.0.1", "127.0.0.3")
	conn := f.intercept(t, origin, "http/1.1")
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	u, err := url.Parse(origin)
	require.NoError(t, err)
	_, err = fmt.Fprintf(conn, "POST /repo/git-receive-pack HTTP/1.1\r\nHost: %s\r\nContent-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", u.Host, len(commands), commands)
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	require.NoError(t, err)
	assertFailureResponse(t, response, 502)
	require.NoError(t, conn.Close())
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, f.engine.Wait(wait))
	require.EqualValues(t, 1, calls.Load())
	require.EqualValues(t, 2, dials.Load())
	observed := f.engine.options.Observations.Status().Protocols[diagnostics.Git]
	require.EqualValues(t, 1, observed.Executions)
	require.EqualValues(t, 1, observed.Results[diagnostics.Unknown])
	// Bounded requested refs survive a post-write failure, but no upstream
	// outcome is inferred and credentials/raw commands remain excluded.
	require.Eventually(t, func() bool {
		h, err := f.traffic.GitHistory(t.Context(), 0, 10)
		return err == nil && len(h.Records) == 1 && h.Records[0].Completion != nil
	}, 3*time.Second, time.Millisecond)
	history, err := f.traffic.GitHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	encoded, err := json.Marshal(history)
	require.NoError(t, err)
	require.False(t, bytes.Contains(encoded, []byte("selected-git-canary")))
	require.False(t, bytes.Contains(encoded, []byte(strings.Repeat("1", 40))))
	require.False(t, bytes.Contains(encoded, []byte(strings.Repeat("0", 40))))
	require.False(t, bytes.Contains(encoded, []byte("report-status")))
	record := history.Records[0]
	require.Equal(t, &contract.GitTrafficRefEvidence{State: "complete", Refs: []contract.GitTrafficRequestedRef{{Name: "refs/heads/private", Action: "delete"}}}, record.Admission.RefEvidence)
	require.Nil(t, record.Completion.RefOutcomes)
	require.Empty(t, record.Completion.ReportedResult)
}
