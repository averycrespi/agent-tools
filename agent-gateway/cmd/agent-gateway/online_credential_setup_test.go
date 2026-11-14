package main

import (
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestCLICredentialSetupStatusAndClosedState(t *testing.T) {
	for _, state := range []contract.CredentialMaterialState{contract.CredentialNotConfigured, contract.CredentialMaterialReady, contract.CredentialMaterialUnavailable} {
		http := httpCredentialCLIResource()
		http.MaterialState = state
		http.Available = state == contract.CredentialMaterialReady
		raw, err := json.Marshal(http)
		require.NoError(t, err)
		table, err := httpCredentialItemTable(raw)
		require.NoError(t, err)
		require.Equal(t, state.Label(), table.Rows[0][6])
		git := gitCLIResource()
		git.MaterialState = state
		git.Available = http.Available
		raw, err = json.Marshal(git)
		require.NoError(t, err)
		table, err = gitCredentialTable(raw)
		require.NoError(t, err)
		require.Equal(t, state.Label(), table.Rows[0][4])
	}
	http := httpCredentialCLIResource()
	http.MaterialState = contract.CredentialNotConfigured
	raw, err := json.Marshal(http)
	require.NoError(t, err)
	_, err = httpCredentialItemTable(raw)
	require.Error(t, err, "ready/unset contradiction must refuse")
	for _, mode := range []string{"bearer", "oauth"} {
		server := serverWire{CredentialState: contract.ServerCredentialAbsent, CredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthTokens: "0"}, Transport: json.RawMessage(`{"authentication":{"mode":"` + mode + `"}}`)}
		want := "Not configured"
		if mode == "oauth" {
			want = "Sign-in required"
		}
		require.Equal(t, want, serverCredentialLabel(server))
	}
}
