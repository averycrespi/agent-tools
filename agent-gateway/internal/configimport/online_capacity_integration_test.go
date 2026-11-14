//go:build integration

package configimport

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationOnlineConfigurationCumulativeCapacityAndLocatorConflict(t *testing.T) {
	s, store, ctx := onlineFixture(t)
	agent := onlineGraph()[0]
	defs := []contract.ConfigDefinition{}
	for n := range 128 {
		d := agent
		d.Key = fmt.Sprintf("agent-%d", n)
		defs = append(defs, d)
	}
	_, err := s.Preview(ctx, submission(t, agent))
	require.NoError(t, err)
	_, err = s.Import(ctx, submission(t, defs...))
	require.NoError(t, err)
	before := count(t, store, ctx, "control_audit_events")
	_, err = s.Import(ctx, submission(t, agent))
	require.Error(t, err)
	require.Equal(t, 128, count(t, store, ctx, "principals"))
	require.Equal(t, before, count(t, store, ctx, "control_audit_events"))
	repo := definition("git_repository", "one", `{"name":"One","url":"https://example.com:443/repo","aliases":[],"credential":null,"deleted":false}`)
	_, err = s.Import(ctx, submission(t, repo))
	require.NoError(t, err)
	repo.Key = "two"
	_, err = s.Preview(ctx, submission(t, repo))
	require.Error(t, err)
	_, err = s.Import(ctx, submission(t, repo))
	require.Error(t, err)
	require.Equal(t, 1, count(t, store, ctx, "git_repositories"))
}

func TestIntegrationOnlineConfigurationCredentialRowAndLiveCapacity(t *testing.T) {
	for _, kind := range []string{"http_credential", "git_credential"} {
		t.Run(kind, func(t *testing.T) {
			s, store, ctx := onlineFixture(t)
			table := "http_credentials"
			if kind == "git_credential" {
				table = "git_credentials"
			}
			makeDefinition := func(n int, deleted bool) contract.ConfigDefinition {
				key := fmt.Sprintf("credential-%d", n)
				secret := "null"
				if !deleted {
					secret = fmt.Sprintf("%q", kind+"/"+key+"/secret")
				}
				spec := fmt.Sprintf(`{"name":"Credential","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":%t,"secret_ref":%s}`, deleted, secret)
				if kind == "git_credential" {
					spec = fmt.Sprintf(`{"name":"Credential","origin":"https://example.com:443","recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":%t,"secret_ref":%s}`, deleted, secret)
				}
				return definition(kind, key, spec)
			}
			live := []contract.ConfigDefinition{}
			for n := range 256 {
				live = append(live, makeDefinition(n, false))
			}
			_, err := s.Import(ctx, submission(t, live...))
			require.NoError(t, err)
			_, err = s.Preview(ctx, submission(t, makeDefinition(256, false)))
			require.Error(t, err)
			_, err = s.Import(ctx, submission(t, makeDefinition(256, false)))
			require.Error(t, err)
			require.Equal(t, 256, count(t, store, ctx, table))
			retained := []contract.ConfigDefinition{}
			for n := 256; n < 1024; n++ {
				retained = append(retained, makeDefinition(n, true))
			}
			_, err = s.Import(ctx, submission(t, retained...))
			require.NoError(t, err)
			require.Equal(t, 1024, count(t, store, ctx, table))
			reserved := contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: kind, Key: "key-only", Reserved: true, Spec: json.RawMessage("null")}
			_, err = s.Import(ctx, submission(t, reserved))
			require.NoError(t, err)
			require.Equal(t, 1024, count(t, store, ctx, table))
			before := count(t, store, ctx, "control_audit_events")
			_, err = s.Import(ctx, submission(t, makeDefinition(1024, true)))
			require.Error(t, err)
			require.Equal(t, 1024, count(t, store, ctx, table))
			require.Equal(t, before, count(t, store, ctx, "control_audit_events"))
			require.Zero(t, count(t, store, ctx, "secret_generations"))
		})
	}
}
