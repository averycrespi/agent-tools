package keyring

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
)

func TestDarwinProbeMappingsAreDeterministicAndNoninteractive(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		errors []error
		state  contract.KeyringCapability
	}{
		"ready":                    {errors: []error{nil, nil}, state: contract.KeyringReady},
		"absent":                   {errors: []error{errBackendAbsent}, state: contract.KeyringAbsent},
		"locked":                   {errors: []error{nil, errBackendLocked}, state: contract.KeyringLocked},
		"unsupported":              {errors: []error{errSecurityToolMissing}, state: contract.KeyringUnsupported},
		"unknown default failure":  {errors: []error{errors.New("native failure")}, state: contract.KeyringUnavailable},
		"unknown keychain failure": {errors: []error{nil, errors.New("native failure")}, state: contract.KeyringUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeSecurityRunner{errors: testCase.errors}
			err := probeDarwin(context.Background(), runner)
			capability := capabilityForPlatformProbe(err)
			assert.Equal(t, testCase.state, capability.State)
			for _, arguments := range runner.arguments {
				assert.NotContains(t, arguments, "secret")
				assert.NotContains(t, arguments, "password")
			}
		})
	}
}

func TestDarwinSecurityExitMappingIsConservative(t *testing.T) {
	t.Parallel()

	assert.ErrorIs(t, classifySecurityExit("default-keychain", securityItemNotFoundExit), errBackendAbsent)
	assert.ErrorIs(t, classifySecurityExit("show-keychain-info", securityAuthFailedExit), errBackendLocked)
	assert.Equal(t, contract.KeyringUnavailable, capabilityForError(classifySecurityExit("default-keychain", 1)).State)
	assert.Equal(t, contract.KeyringUnavailable, capabilityForError(classifySecurityExit("show-keychain-info", 1)).State)
}

func TestLinuxProbeMappingsAreDeterministicAndDismissPrompts(t *testing.T) {
	t.Parallel()

	nativeFailure := errors.New("native canary detail")
	for name, testCase := range map[string]struct {
		address    string
		connectErr error
		client     *fakeLinuxProbeClient
		state      contract.KeyringCapability
		dismissed  bool
	}{
		"headless":             {state: contract.KeyringAbsent},
		"session unavailable":  {address: "session", connectErr: nativeFailure, state: contract.KeyringUnavailable},
		"service absent":       {address: "session", client: &fakeLinuxProbeClient{}, state: contract.KeyringAbsent},
		"collection absent":    {address: "session", client: &fakeLinuxProbeClient{owner: true, lockedErr: errSecretCollectionAbsent}, state: contract.KeyringAbsent},
		"ready":                {address: "session", client: &fakeLinuxProbeClient{owner: true}, state: contract.KeyringReady},
		"unlocks without UI":   {address: "session", client: &fakeLinuxProbeClient{owner: true, locked: true, unlocked: true}, state: contract.KeyringReady},
		"locked":               {address: "session", client: &fakeLinuxProbeClient{owner: true, locked: true, unlockErr: errBackendLocked}, state: contract.KeyringLocked},
		"unlock failure":       {address: "session", client: &fakeLinuxProbeClient{owner: true, locked: true, unlockErr: nativeFailure}, state: contract.KeyringUnavailable},
		"interaction required": {address: "session", client: &fakeLinuxProbeClient{owner: true, locked: true, prompt: true}, state: contract.KeyringInteractionRequired, dismissed: true},
		"dismiss failure":      {address: "session", client: &fakeLinuxProbeClient{owner: true, locked: true, prompt: true, dismissErr: nativeFailure}, state: contract.KeyringUnavailable, dismissed: true},
		"service failure":      {address: "session", client: &fakeLinuxProbeClient{ownerErr: nativeFailure}, state: contract.KeyringUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			connect := func() (linuxProbeClient, error) {
				if testCase.connectErr != nil {
					return nil, testCase.connectErr
				}
				return testCase.client, nil
			}
			err := probeLinux(context.Background(), testCase.address, connect)
			capability := capabilityForError(err)
			assert.Equal(t, testCase.state, capability.State)
			if testCase.client != nil {
				assert.Equal(t, testCase.dismissed, testCase.client.dismissed)
				assert.True(t, testCase.client.closed)
			}
		})
	}
}

func capabilityForPlatformProbe(err error) Capability {
	if errors.Is(err, errSecurityToolMissing) {
		return Capability{State: contract.KeyringUnsupported, Remediation: RemediationUnsupported}
	}
	return capabilityForError(err)
}

type fakeSecurityRunner struct {
	mu        sync.Mutex
	errors    []error
	arguments [][]string
}

func (runner *fakeSecurityRunner) Run(_ context.Context, arguments ...string) error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.arguments = append(runner.arguments, append([]string(nil), arguments...))
	index := len(runner.arguments) - 1
	if index >= len(runner.errors) {
		return nil
	}
	return runner.errors[index]
}

type fakeLinuxProbeClient struct {
	owner      bool
	ownerErr   error
	locked     bool
	lockedErr  error
	unlocked   bool
	prompt     bool
	unlockErr  error
	dismissed  bool
	dismissErr error
	closed     bool
}

func (client *fakeLinuxProbeClient) HasOwner(context.Context) (bool, error) {
	return client.owner, client.ownerErr
}

func (client *fakeLinuxProbeClient) Locked(context.Context) (bool, error) {
	return client.locked, client.lockedErr
}

func (client *fakeLinuxProbeClient) UnlockWithoutPrompt(context.Context) (bool, bool, error) {
	return client.unlocked, client.prompt, client.unlockErr
}

func (client *fakeLinuxProbeClient) DismissPrompt(context.Context) error {
	client.dismissed = true
	return client.dismissErr
}

func (client *fakeLinuxProbeClient) Close() error {
	client.closed = true
	return nil
}
