package invocation

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestHTTPHistoricalSelectorsSurviveEditAndDeletion(t *testing.T) {
	c, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	policy := json.RawMessage(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"values":["GET"]},"path":{"kind":"segment_prefix","value":"/approved"}}}`)
	grant, err := authority.PutHTTPGrant(t.Context(), "", "", authorization.HTTPGrantInput{PrincipalID: principal.ID, Policy: policy})
	require.NoError(t, err)
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	identity, err := audits.PrepareIdentity()
	require.NoError(t, err)
	result, err := c.AdmitHTTP(t.Context(), lease, identity, authorization.HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com/approved/observed-private-canary?token=query-private-canary", Method: "GET"}, publicHTTPFacts(), nil)
	require.NoError(t, err)
	require.True(t, result.DispatchAuthorized)
	defer result.Settle()
	current, err := authority.GetPrincipal(t.Context(), principal.ID)
	require.NoError(t, err)
	allow := contract.HTTPDefaultAllow
	_, err = authority.PatchPrincipal(t.Context(), principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: current.Revision, HTTPDefault: &allow})
	require.NoError(t, err)
	updated, err := authority.PutHTTPGrant(t.Context(), grant.ID, grant.Revision, authorization.HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(strings.ReplaceAll(string(policy), "/approved", "/changed"))})
	require.NoError(t, err)
	require.NoError(t, authority.DeleteHTTPGrant(t.Context(), updated.ID, updated.Revision))
	waitTraffic(t, audits.traffic)
	reader, err := NewReadService(audits, authority)
	require.NoError(t, err)
	item, err := reader.GetHTTP(t.Context(), identity.InvocationID)
	require.NoError(t, err)
	require.Len(t, item.Admission.Grants, 1)
	require.Equal(t, contract.HTTPDefaultBlock, item.Admission.Default)
	require.EqualValues(t, 1, item.Admission.Decision.DefaultRevision)
	require.Equal(t, grant.ID, item.Admission.Grants[0].Reference.ID)
	require.EqualValues(t, 1, item.Admission.Grants[0].Reference.Revision)
	require.Equal(t, "/approved", item.Admission.Grants[0].Policy.Request.Path.Value)
	require.Nil(t, item.Completion)
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	for _, canary := range []string{"observed-private-canary", "query-private-canary", credential.Bearer} {
		require.NotContains(t, string(encoded), canary)
		for _, path := range []string{audits.traffic.path, audits.traffic.path + "-wal"} {
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			require.NoError(t, err)
			require.NotContains(t, string(raw), canary)
		}
	}
}

func publicHTTPFacts() httppolicy.AddressFacts {
	return httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
}

func TestHTTPOptionalRecordingNeverGrantsOrBlocksExecution(t *testing.T) {
	for _, mode := range []string{"stalled", "full", "unavailable", "uncertain", "invalid capture", "deny", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			c, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
			if mode != "deny" {
				allow := contract.HTTPDefaultAllow
				_, err := authority.PatchPrincipal(t.Context(), principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, HTTPDefault: &allow})
				require.NoError(t, err)
			}
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
			input := authorization.HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com/private-canary?token=query-canary", Method: "GET"}
			if mode == "malformed" {
				input.URL = "https://example.com/%5csecret-canary"
			}
			type outcome struct {
				result HTTPAdmissionResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				r, e := c.AdmitHTTP(t.Context(), lease, identity, input, publicHTTPFacts(), nil)
				done <- outcome{r, e}
			}()
			var got outcome
			select {
			case got = <-done:
			case <-time.After(time.Second):
				t.Fatal("HTTP admission waited for history")
			}
			require.NoError(t, got.err)
			allowed := mode != "deny" && mode != "malformed"
			require.Equal(t, allowed, got.result.DispatchAuthorized)
			if allowed {
				got.result.Settle()
				got.result.Settle()
				terminal := make(chan error, 1)
				go func() { terminal <- c.CompleteHTTP(t.Context(), got.result, httpTrafficCompletion()) }()
				select {
				case <-terminal:
				case <-time.After(time.Second):
					t.Fatal("HTTP completion waited for history")
				}
			}
			unblock()
			waitTraffic(t, traffic)
			if mode == "uncertain" {
				require.False(t, traffic.Healthy())
			}
			history, err := traffic.HTTPHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			for _, record := range history.Records {
				raw, err := encodeHTTPAdmission(record.Admission)
				require.NoError(t, err)
				for _, canary := range []string{"private-canary", "query-canary", "secret-canary"} {
					require.NotContains(t, raw, canary)
				}
			}
		})
	}
}
