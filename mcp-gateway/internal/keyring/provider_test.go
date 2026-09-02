package keyring

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	oskeyring "github.com/zalando/go-keyring"
)

const (
	testInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testOwnerID        = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
)

func TestNamespaceValidation(t *testing.T) {
	t.Parallel()

	valid, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthClient)
	require.NoError(t, err)
	assert.Equal(t, testInstallationID, valid.InstallationID())
	assert.Equal(t, testOwnerID, valid.Owner())
	assert.Equal(t, RecordOAuthClient, valid.Kind())

	for name, input := range map[string]struct {
		installation string
		owner        string
		kind         RecordKind
	}{
		"invalid installation": {installation: "not-an-installation", owner: testOwnerID, kind: RecordStaticCredential},
		"empty owner":          {installation: testInstallationID, owner: "", kind: RecordStaticCredential},
		"owner separator":      {installation: testInstallationID, owner: "owner/child", kind: RecordStaticCredential},
		"empty kind":           {installation: testInstallationID, owner: testOwnerID, kind: ""},
		"unknown kind":         {installation: testInstallationID, owner: testOwnerID, kind: "unknown"},
		"owner too long":       {installation: testInstallationID, owner: testOwnerID + "0", kind: RecordStaticCredential},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewNamespace(input.installation, input.owner, input.kind)
			require.Error(t, err)
		})
	}
}

func TestProbeDoesNotInvokeSecretOperations(t *testing.T) {
	t.Parallel()

	adapter := &fakeAdapter{}
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)

	assert.Equal(t, contract.KeyringReady, provider.Probe(context.Background()).State)
	setCalls, getCalls, deleteCalls := adapter.operationCallCounts()
	assert.Zero(t, setCalls)
	assert.Zero(t, getCalls)
	assert.Zero(t, deleteCalls)
}

func TestProbeMapsEveryCapabilityWithoutNativeDiagnostics(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		err         error
		state       contract.KeyringCapability
		remediation Remediation
	}{
		"ready":                {state: contract.KeyringReady, remediation: RemediationNone},
		"absent":               {err: errBackendAbsent, state: contract.KeyringAbsent, remediation: RemediationInstall},
		"locked":               {err: errBackendLocked, state: contract.KeyringLocked, remediation: RemediationUnlock},
		"interaction required": {err: errBackendInteractionRequired, state: contract.KeyringInteractionRequired, remediation: RemediationAuthorize},
		"unavailable":          {err: errors.New("native canary detail"), state: contract.KeyringUnavailable, remediation: RemediationRetry},
		"unsupported sentinel": {err: oskeyring.ErrUnsupportedPlatform, state: contract.KeyringUnsupported, remediation: RemediationUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			adapter := &fakeAdapter{probeErr: testCase.err}
			provider, err := newProviderWithAdapter(testInstallationID, adapter)
			require.NoError(t, err)

			capability := provider.Probe(context.Background())

			assert.Equal(t, testCase.state, capability.State)
			assert.Equal(t, testCase.remediation, capability.Remediation)
			assert.NotContains(t, capability.String(), "native canary")
			assert.Equal(t, gatewayServicePrefix+testInstallationID, adapter.lastService())
		})
	}
}

func TestProviderOperationsClassifyFailuresAndNeverFallback(t *testing.T) {
	t.Parallel()

	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	for name, testCase := range map[string]struct {
		err        error
		target     error
		capability contract.KeyringCapability
	}{
		"missing item": {err: oskeyring.ErrNotFound, target: ErrNotFound},
		"too large":    {err: oskeyring.ErrSetDataTooBig, target: ErrSecretTooLarge},
		"absent":       {err: errBackendAbsent, capability: contract.KeyringAbsent},
		"locked":       {err: errBackendLocked, capability: contract.KeyringLocked},
		"interaction":  {err: errBackendInteractionRequired, capability: contract.KeyringInteractionRequired},
		"unsupported":  {err: oskeyring.ErrUnsupportedPlatform, capability: contract.KeyringUnsupported},
		"unavailable":  {err: errors.New("native canary detail"), capability: contract.KeyringUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			adapter := &fakeAdapter{getErr: testCase.err, setErr: testCase.err, deleteErr: testCase.err}
			provider, err := newProviderWithAdapter(testInstallationID, adapter)
			require.NoError(t, err)

			_, getErr := provider.get(context.Background(), namespace, "item")
			setErr := provider.set(context.Background(), namespace, "item", "secret canary")
			deleteErr := provider.delete(context.Background(), namespace, "item")
			for _, operationErr := range []error{getErr, setErr, deleteErr} {
				require.Error(t, operationErr)
				assert.NotContains(t, operationErr.Error(), "native canary")
				if testCase.target != nil {
					assert.ErrorIs(t, operationErr, testCase.target)
					continue
				}
				var capabilityErr *CapabilityError
				require.ErrorAs(t, operationErr, &capabilityErr)
				assert.Equal(t, testCase.capability, capabilityErr.Capability.State)
			}
			assert.Empty(t, adapter.fallbackWrites)
		})
	}
}

func TestCapabilityFailureClosesDependentOperations(t *testing.T) {
	t.Parallel()

	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	adapter := &fakeAdapter{probeErr: errBackendInteractionRequired}
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)

	err = provider.set(context.Background(), namespace, "item", "secret canary")

	var capabilityErr *CapabilityError
	require.ErrorAs(t, err, &capabilityErr)
	assert.Equal(t, contract.KeyringInteractionRequired, capabilityErr.Capability.State)
	assert.Zero(t, adapter.setCalls)
}

func TestSystemAdapterConformanceUsesGoKeyringInIsolatedProcess(t *testing.T) {
	if os.Getenv("MCP_GATEWAY_KEYRING_CONFORMANCE_HELPER") == "1" {
		oskeyring.MockInit()
		adapter := systemAdapter{}
		require.NoError(t, adapter.Set("mcp-gateway-conformance", "item", "value"))
		value, err := adapter.Get("mcp-gateway-conformance", "item")
		require.NoError(t, err)
		assert.Equal(t, "value", value)
		require.NoError(t, adapter.Delete("mcp-gateway-conformance", "item"))
		_, err = adapter.Get("mcp-gateway-conformance", "item")
		assert.ErrorIs(t, err, oskeyring.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSystemAdapterConformanceUsesGoKeyringInIsolatedProcess$")
	command.Env = append(os.Environ(), "MCP_GATEWAY_KEYRING_CONFORMANCE_HELPER=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestInjectedAdaptersAreInstanceLocal(t *testing.T) {
	t.Parallel()

	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	firstProvider, err := newProviderWithAdapter(testInstallationID, &fakeAdapter{value: "first"})
	require.NoError(t, err)
	secondProvider, err := newProviderWithAdapter(testInstallationID, &fakeAdapter{value: "second"})
	require.NoError(t, err)

	first, err := firstProvider.get(context.Background(), namespace, "item")
	require.NoError(t, err)
	second, err := secondProvider.get(context.Background(), namespace, "item")
	require.NoError(t, err)
	assert.Equal(t, "first", first)
	assert.Equal(t, "second", second)
}

func TestSharedWorkLimiterRejectsCrossProviderNPlusOneWithoutQueuing(t *testing.T) {
	t.Parallel()

	firstNamespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	secondNamespace, err := NewNamespace(testInstallationID, "01ARZ3NDEKTSV4RRFFQ69G5FAX", RecordOAuthClient)
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	firstAdapter := &fakeAdapter{value: "first", getStarted: started, releaseGet: release}
	secondAdapter := &fakeAdapter{value: "second"}
	limiter := newWorkLimiter()
	firstProvider, err := newProviderWithAdapterAndLimiter(testInstallationID, firstAdapter, limiter)
	require.NoError(t, err)
	secondProvider, err := newProviderWithAdapterAndLimiter(testInstallationID, secondAdapter, limiter)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, getErr := firstProvider.get(ctx, firstNamespace, "item")
		result <- getErr
	}()
	<-started

	assert.Equal(t, contract.LimitStatus{InUse: 1, Limit: 1, Saturated: true}, firstProvider.WorkStatus())
	_, err = secondProvider.get(context.Background(), secondNamespace, "item")
	assert.ErrorIs(t, err, ErrWorkLimit)
	assert.Zero(t, secondAdapter.getCallCount())
	cancel()
	assert.True(t, secondProvider.WorkStatus().Saturated)

	close(release)
	require.NoError(t, <-result)
	assert.Equal(t, contract.LimitStatus{InUse: 0, Limit: 1, Saturated: false}, firstProvider.WorkStatus())
}

type fakeAdapter struct {
	mu             sync.Mutex
	probeErr       error
	getErr         error
	setErr         error
	deleteErr      error
	value          string
	service        string
	setCalls       int
	getCalls       int
	deleteCalls    int
	getStarted     chan struct{}
	releaseGet     chan struct{}
	getStartedOnce sync.Once
	fallbackWrites []string
}

func (adapter *fakeAdapter) Probe(_ context.Context, service string) error {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.service = service
	return adapter.probeErr
}

func (adapter *fakeAdapter) Set(_, _, _ string) error {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.setCalls++
	return adapter.setErr
}

func (adapter *fakeAdapter) Get(_, _ string) (string, error) {
	adapter.mu.Lock()
	adapter.getCalls++
	value := adapter.value
	err := adapter.getErr
	started := adapter.getStarted
	release := adapter.releaseGet
	adapter.mu.Unlock()
	if started != nil {
		adapter.getStartedOnce.Do(func() { close(started) })
	}
	if release != nil {
		<-release
	}
	return value, err
}

func (adapter *fakeAdapter) Delete(_, _ string) error {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.deleteCalls++
	return adapter.deleteErr
}

func (adapter *fakeAdapter) operationCallCounts() (int, int, int) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.setCalls, adapter.getCalls, adapter.deleteCalls
}

func (adapter *fakeAdapter) getCallCount() int {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.getCalls
}

func (adapter *fakeAdapter) lastService() string {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.service
}
