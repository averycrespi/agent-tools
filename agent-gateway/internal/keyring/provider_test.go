package keyring

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

const (
	testInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testOwnerID        = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
)

func TestNamespaceValidation(t *testing.T) {
	valid, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthClient)
	require.NoError(t, err)
	require.Equal(t, testInstallationID, valid.InstallationID())
	require.Equal(t, testOwnerID, valid.Owner())
	require.Equal(t, RecordOAuthClient, valid.Kind())
	for _, input := range []struct {
		installation, owner string
		kind                RecordKind
	}{
		{"not-an-installation", testOwnerID, RecordStaticCredential},
		{testInstallationID, "", RecordStaticCredential},
		{testInstallationID, "owner/child", RecordStaticCredential},
		{testInstallationID, testOwnerID, ""},
		{testInstallationID, testOwnerID, "unknown"},
		{testInstallationID, testOwnerID + "0", RecordStaticCredential},
	} {
		_, err := NewNamespace(input.installation, input.owner, input.kind)
		require.Error(t, err)
	}
}

func TestProviderRequiresConfiguredEncryptedCustody(t *testing.T) {
	provider, err := NewProvider(testInstallationID)
	require.NoError(t, err)
	require.Equal(t, Capability{State: contract.KeyringUnavailable, Remediation: RemediationRetry}, provider.Probe(t.Context()))
	_, _, configured := custodyFixture(t)
	require.Equal(t, Capability{State: contract.KeyringReady, Remediation: RemediationNone}, configured.Probe(t.Context()))
}

func TestGenerationWorkLimiterRejectsNPlusOneWithoutQueuing(t *testing.T) {
	first, err := NewProvider(testInstallationID)
	require.NoError(t, err)
	second := first
	release, err := first.acquireWork()
	require.NoError(t, err)
	defer release()
	require.True(t, second.WorkStatus().Saturated)
	_, err = second.acquireWork()
	require.ErrorIs(t, err, ErrWorkLimit)
}
