package runtimes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type driverWriteCloser struct{ bytes.Buffer }

func (*driverWriteCloser) Close() error { return nil }

type driverStdioRuntime struct {
	frames    chan []byte
	input     *driverWriteCloser
	stop      bool
	stopCalls int
}

func newDriverStdioRuntime(stop bool) *driverStdioRuntime {
	return &driverStdioRuntime{frames: make(chan []byte), input: new(driverWriteCloser), stop: stop}
}

func (runtime *driverStdioRuntime) Frames() <-chan []byte { return runtime.frames }
func (runtime *driverStdioRuntime) Input() io.WriteCloser { return runtime.input }
func (runtime *driverStdioRuntime) Stop(context.Context) bool {
	runtime.stopCalls++
	return runtime.stop
}

func TestConcreteDriverOwnsStdioBeforeConstructionAndStopsExactly(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(60, contract.TransportStdio)
	candidate.Server.Transport = []byte(`{"kind":"stdio","executable":"/bin/server","arguments":["--flag"],"working_directory":"/tmp","environment":{"SAFE":"value"},"secret_environment":{"TOKEN":"api"}}`)
	generation, err := servercredentials.EncodeStaticGeneration(map[string]string{"api": "stdio-canary"})
	require.NoError(t, err)
	lease, err := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: generation})
	require.NoError(t, err)
	process := newDriverStdioRuntime(true)
	var definition StdioDefinition
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner: owner,
		StartStdio: func(_ context.Context, received StdioDefinition) (downstream.StdioRuntime, error) {
			phase, ok := owner.Phase(candidate.Key())
			require.True(t, ok)
			assert.Equal(t, RuntimeConstructing, phase)
			definition = received
			return process, nil
		},
		HTTPFactory: remote.New(remote.Options{}),
	})
	require.NoError(t, err)

	outcome := driver.Reconcile(context.Background(), candidate, lease)

	assert.Equal(t, contract.RuntimeDegraded, outcome.State)
	require.NotNil(t, outcome.Reason)
	assert.Equal(t, contract.ReasonProtocolUnsupported, *outcome.Reason)
	assert.Equal(t, candidate.RuntimeID, definition.RuntimeID)
	assert.Equal(t, "/bin/server", definition.Executable)
	assert.Equal(t, []string{"--flag"}, definition.Arguments)
	assert.Equal(t, map[string]string{"SAFE": "value"}, definition.Environment)
	assert.Equal(t, map[string]string{"TOKEN": "api"}, definition.SecretEnvironment)
	assert.Equal(t, map[string]string{"api": "stdio-canary"}, definition.Secrets)
	phase, ok := owner.Phase(candidate.Key())
	require.True(t, ok)
	assert.Equal(t, RuntimeNegotiating, phase)
	coordinator, ok := driver.Coordinator(candidate)
	require.True(t, ok)
	assert.Equal(t, downstream.TransportStdio, coordinator.Kind())

	mismatch := candidate
	mismatch.Generation++
	assert.False(t, driver.Stop(context.Background(), mismatch))
	assert.True(t, driver.Stop(context.Background(), candidate))
	assert.Equal(t, 1, process.stopCalls)
	assert.Equal(t, int64(0), owner.Status().InUse)
	assert.False(t, driver.Stop(context.Background(), candidate))
}

func TestConcreteDriverConstructsHardenedHTTPAuthorization(t *testing.T) {
	tests := []struct {
		name           string
		authentication contract.HTTPAuthentication
		lease          func(Candidate) *MaterialLease
		wantAuth       string
	}{
		{name: "none", authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}},
		{name: "bearer", authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}, wantAuth: "Bearer bearer-canary", lease: func(candidate Candidate) *MaterialLease {
			generation, _ := servercredentials.EncodeStaticGeneration(map[string]string{"bearer": "bearer-canary"})
			lease, _ := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: generation})
			return lease
		}},
		{name: "oauth", authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}, TrustedOrigins: []string{}}, wantAuth: "Bearer oauth-canary", lease: func(candidate Candidate) *MaterialLease {
			lease, _ := NewOAuthMaterialLease(candidate.Key(), nil, []byte("oauth-canary"), OAuthMaterialMetadata{Scopes: []string{"read"}, ScopeSpecified: true})
			return lease
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := ownerCandidate(70+index, contract.TransportStreamableHTTP)
			url := "https://resource.example/mcp"
			if test.name == "none" {
				url = "http://127.0.0.1:9000/mcp"
			}
			candidate.Server.Transport = mustDriverTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: url, ProtocolMode: contract.ProtocolModern, Authentication: test.authentication})
			var lease *MaterialLease
			if test.lease != nil {
				lease = test.lease(candidate)
			}
			driver, err := NewConcreteDriver(ConcreteDriverOptions{Owner: NewRuntimeOwner(), StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
				return nil, errors.New("unexpected stdio")
			}, HTTPFactory: remote.New(remote.Options{})})
			require.NoError(t, err)
			outcome := driver.Reconcile(context.Background(), candidate, lease)
			assert.Equal(t, contract.RuntimeDegraded, outcome.State)
			coordinator, ok := driver.Coordinator(candidate)
			require.True(t, ok)
			assert.Equal(t, downstream.TransportHTTP, coordinator.Kind())
			driver.mu.Lock()
			handle := driver.handles[candidate.Key()]
			driver.mu.Unlock()
			require.NotNil(t, handle)
			handle.mu.Lock()
			assert.Equal(t, test.wantAuth, handle.authorization)
			handle.mu.Unlock()
			assert.True(t, driver.Stop(context.Background(), candidate))
		})
	}
}

func TestConcreteDriverSharesMixedRuntimeCapacity(t *testing.T) {
	owner := NewRuntimeOwner()
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner: owner,
		StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
			return newDriverStdioRuntime(true), nil
		},
		HTTPFactory: remote.New(remote.Options{}),
	})
	require.NoError(t, err)
	candidates := make([]Candidate, 0, 32)
	for index := range 32 {
		candidate := ownerCandidate(100+index, contract.TransportStreamableHTTP)
		if index%2 == 0 {
			candidate.Server.Transport = []byte(`{"kind":"stdio","executable":"/bin/server","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}`)
		}
		outcome := driver.Reconcile(context.Background(), candidate, nil)
		assert.Equal(t, contract.RuntimeDegraded, outcome.State)
		candidates = append(candidates, candidate)
	}
	assert.True(t, owner.Status().Saturated)
	overflow := ownerCandidate(132, contract.TransportStreamableHTTP)
	outcome := driver.Reconcile(context.Background(), overflow, nil)
	require.NotNil(t, outcome.Reason)
	assert.Equal(t, contract.ReasonResourceLimit, *outcome.Reason)
	assert.True(t, outcome.Retryable)
	for _, candidate := range candidates {
		assert.True(t, driver.Stop(context.Background(), candidate))
	}
	assert.Equal(t, int64(0), owner.Status().InUse)
}

func TestConcreteDriverReleasesVerifiedConstructionFailure(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(79, contract.TransportStdio)
	generation, err := servercredentials.EncodeStaticGeneration(map[string]string{"api": "cleanup-canary"})
	require.NoError(t, err)
	lease, err := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: generation})
	require.NoError(t, err)
	process := newDriverStdioRuntime(true)
	var retained []byte
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner:       owner,
		StartStdio:  func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) { return process, nil },
		HTTPFactory: remote.New(remote.Options{}),
		NewCoordinator: func(downstream.Transport) (*downstream.Coordinator, error) {
			retained, _ = owner.Material(candidate.Key(), contract.ServerCredentialStatic)
			return nil, errors.New("post-start failure")
		},
	})
	require.NoError(t, err)

	outcome := driver.Reconcile(context.Background(), candidate, lease)

	assert.Equal(t, contract.RuntimeDegraded, outcome.State)
	assert.Equal(t, 1, process.stopCalls)
	assert.Equal(t, int64(0), owner.Status().InUse)
	assert.Equal(t, make([]byte, len(retained)), retained)
	assert.False(t, driver.Stop(context.Background(), candidate))
}

func TestConcreteDriverRetainsBlockedConstructionAndMaterial(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(80, contract.TransportStdio)
	generation, err := servercredentials.EncodeStaticGeneration(map[string]string{"api": "blocked-canary"})
	require.NoError(t, err)
	lease, err := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: generation})
	require.NoError(t, err)
	process := newDriverStdioRuntime(false)
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner:       owner,
		StartStdio:  func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) { return process, nil },
		HTTPFactory: remote.New(remote.Options{}),
		NewCoordinator: func(downstream.Transport) (*downstream.Coordinator, error) {
			return nil, errors.New("post-start failure")
		},
	})
	require.NoError(t, err)

	outcome := driver.Reconcile(context.Background(), candidate, lease)

	assert.Equal(t, contract.RuntimeDegraded, outcome.State)
	assert.Equal(t, 1, process.stopCalls)
	phase, ok := owner.Phase(candidate.Key())
	require.True(t, ok)
	assert.Equal(t, RuntimeBlockedStop, phase)
	material, ok := owner.Material(candidate.Key(), contract.ServerCredentialStatic)
	require.True(t, ok)
	assert.Contains(t, string(material), "blocked-canary")
	assert.False(t, driver.Stop(context.Background(), candidate))
}

func mustDriverTransport(t *testing.T, transport contract.Transport) []byte {
	t.Helper()
	encoded, err := json.Marshal(transport)
	require.NoError(t, err)
	return encoded
}

var _ io.WriteCloser = (*driverWriteCloser)(nil)
