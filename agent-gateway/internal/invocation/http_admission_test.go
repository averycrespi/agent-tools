package invocation

import (
	"context"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPHistoricalSelectorsSurviveEditAndDeletion(t *testing.T) {
	coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, nil, nil)
	audits.traffic = traffic
	policy := json.RawMessage(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"values":["GET"]},"path":{"kind":"segment_prefix","value":"/approved"}}}`)
	grant, err := authority.PutHTTPGrant(t.Context(), "", "", authorization.HTTPGrantInput{PrincipalID: principal.ID, Policy: policy})
	require.NoError(t, err)
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	identity, err := audits.PrepareIdentity()
	require.NoError(t, err)
	result, err := coordinator.AdmitHTTP(t.Context(), lease, identity, authorization.HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com/approved/observed-private-canary?token=query-private-canary", Method: "GET"}, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, nil)
	require.NoError(t, err)
	require.True(t, result.DispatchAuthorized)
	updated, err := authority.PutHTTPGrant(t.Context(), grant.ID, grant.Revision, authorization.HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(strings.ReplaceAll(string(policy), "/approved", "/changed"))})
	require.NoError(t, err)
	require.NoError(t, authority.DeleteHTTPGrant(t.Context(), updated.ID, updated.Revision))
	reader, err := NewReadService(audits, authority)
	require.NoError(t, err)
	item, err := reader.GetHTTP(t.Context(), identity.InvocationID)
	require.NoError(t, err)
	require.Len(t, item.Admission.Grants, 1)
	assert.Equal(t, grant.ID, item.Admission.Grants[0].Reference.ID)
	assert.EqualValues(t, 1, item.Admission.Grants[0].Reference.Revision)
	assert.Equal(t, "/approved", item.Admission.Grants[0].Policy.Request.Path.Value)
	assert.Nil(t, item.Completion)
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	for _, canary := range []string{"observed-private-canary", "query-private-canary", credential.Bearer} {
		assert.NotContains(t, string(encoded), canary)
		for _, path := range []string{traffic.path, traffic.path + "-wal"} {
			raw, readErr := os.ReadFile(path)
			if os.IsNotExist(readErr) {
				continue
			}
			require.NoError(t, readErr)
			assert.NotContains(t, string(raw), canary)
		}
	}
}

func TestHTTPReceiptAdmissionConfirmationRaces(t *testing.T) {
	for _, mode := range []string{"allow", "revoke", "policy", "cancel", "drain", "lost acknowledgment", "malformed", "deny"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
			def, err := authority.GetHTTPDefault(ctx, principal.ID)
			require.NoError(t, err)
			if mode != "deny" {
				_, err = authority.SetHTTPDefault(ctx, principal.ID, def.Revision, contract.HTTPDefaultAllow)
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
					return errors.New("uncertain acknowledgment")
				}
				return nil
			})
			audits.traffic = traffic
			lease, err := authority.Authenticate(ctx, credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			input := authorization.HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com/private-canary?token=query-canary", Method: "GET"}
			if mode == "malformed" {
				input.URL = "https://example.com/%2fsecret-canary"
			}
			facts := httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
			resultCh := make(chan HTTPAdmissionResult, 1)
			errCh := make(chan error, 1)
			go func() {
				result, err := coordinator.AdmitHTTP(ctx, lease, identity, input, facts, nil)
				resultCh <- result
				errCh <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("traffic writer did not enter")
			}
			// These operations must settle while persistence is held: no authority is
			// retained across the writer barrier.
			switch mode {
			case "revoke":
				_, err = authority.RevokeCredential(ctx, principal.ID, credential.Principal.Revision)
				require.NoError(t, err)
			case "policy":
				current, readErr := authority.GetHTTPDefault(ctx, principal.ID)
				require.NoError(t, readErr)
				_, err = authority.SetHTTPDefault(ctx, principal.ID, current.Revision, contract.HTTPDefaultBlock)
				require.NoError(t, err)
			case "cancel":
				cancel()
			case "drain":
				traffic.BeginDrain()
			}
			unblock()
			result, err := <-resultCh, <-errCh
			dispatched := 0
			if result.DispatchAuthorized {
				dispatched++
				require.NoError(t, coordinator.CompleteHTTP(t.Context(), result, httpTrafficCompletion()))
			}
			if mode == "allow" {
				require.NoError(t, err)
				assert.Equal(t, 1, dispatched)
			} else {
				assert.Zero(t, dispatched)
			}
			if mode == "malformed" || mode == "deny" {
				require.NoError(t, err)
				require.True(t, result.Committed)
			}
			if mode == "lost acknowledgment" {
				require.False(t, result.Committed)
				require.Error(t, err)
			}
			history, readErr := traffic.HTTPHistory(t.Context(), 0, 10)
			require.NoError(t, readErr)
			if result.Committed {
				require.Len(t, history.Records, 1)
			}
			for _, record := range history.Records {
				raw, encodeErr := encodeHTTPAdmission(record.Admission)
				require.NoError(t, encodeErr)
				for _, canary := range []string{"private-canary", "query-canary", "secret-canary"} {
					assert.False(t, strings.Contains(raw, canary))
				}
			}
		})
	}
}
