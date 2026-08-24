package runtimes

import (
	"fmt"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ownerCandidate(index int, transport contract.TransportKind) Candidate {
	contents := []byte(`{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"api"}}`)
	if transport == contract.TransportStreamableHTTP {
		contents = []byte(`{"kind":"streamable_http","url":"http://127.0.0.1:9000/mcp","protocol_mode":"modern","authentication":{"mode":"none"}}`)
	}
	return Candidate{
		Server: servers.Server{
			ID:              fmt.Sprintf("server-%d", index),
			DesiredRevision: fmt.Sprintf("%d", index+1),
			Transport:       contents,
		},
		Authority: servers.AuthorityMetadata{
			RegistrationRevision: fmt.Sprintf("%d", index+2),
			CredentialRevisions: contract.CredentialRevisions{
				StaticCredential: fmt.Sprintf("%d", index+3),
				OAuthClient:      fmt.Sprintf("%d", index+4),
				OAuthTokens:      fmt.Sprintf("%d", index+5),
			},
		},
		RuntimeID:  fmt.Sprintf("runtime-%d", index),
		Generation: uint64(index + 6),
		DrainEpoch: uint64(index + 7),
	}
}

func TestCandidateKeyBindsOnlyRelevantAuthority(t *testing.T) {
	credentialFree := ownerCandidate(0, contract.TransportStreamableHTTP)
	credentialFreeKey := credentialFree.Key()
	credentialFree.Authority.RegistrationRevision = "different"
	credentialFree.Authority.CredentialRevisions = contract.CredentialRevisions{StaticCredential: "different", OAuthClient: "different", OAuthTokens: "different"}
	assert.Equal(t, credentialFreeKey, credentialFree.Key())

	static := ownerCandidate(1, contract.TransportStdio)
	staticKey := static.Key()
	static.Authority.CredentialRevisions.StaticCredential = "different"
	assert.NotEqual(t, staticKey, static.Key())

	handle := "client-handle"
	oauth := ownerCandidate(2, contract.TransportStreamableHTTP)
	oauth.Server.Transport = []byte(`{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"oauth","registration":{"mode":"static","client_id":"client","token_endpoint_auth_method":"client_secret_basic"},"trusted_origins":[],"request_offline_access":false}}`)
	oauth.Authority.OAuthClientHandle = &handle
	oauthKey := oauth.Key()
	registrationMismatch := oauth
	registrationMismatch.Authority.RegistrationRevision = "different"
	clientMismatch := oauth
	clientMismatch.Authority.CredentialRevisions.OAuthClient = "different"
	tokenMismatch := oauth
	tokenMismatch.Authority.CredentialRevisions.OAuthTokens = "different"
	assert.NotEqual(t, oauthKey, registrationMismatch.Key())
	assert.NotEqual(t, oauthKey, clientMismatch.Key())
	assert.NotEqual(t, oauthKey, tokenMismatch.Key())
}

func TestRuntimeOwnerAdmitsMixedCandidatesBeforeConstruction(t *testing.T) {
	owner := NewRuntimeOwner()
	for index := range 32 {
		transport := contract.TransportStdio
		if index%2 == 1 {
			transport = contract.TransportStreamableHTTP
		}
		candidate := ownerCandidate(index, transport)
		key, err := owner.Admit(candidate, nil, func(owned OwnedRuntime) error {
			assert.Equal(t, candidate.Key(), owned.Key())
			phase, ok := owner.Phase(candidate.Key())
			assert.True(t, ok)
			assert.Equal(t, RuntimeConstructing, phase)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, candidate.Key(), key)
	}
	status := owner.Status()
	assert.Equal(t, int64(32), status.InUse)
	assert.True(t, status.Saturated)
	_, err := owner.Admit(ownerCandidate(32, contract.TransportStdio), nil, nil)
	assert.ErrorIs(t, err, ErrRuntimeOwnerLimit)
}

func TestRuntimeOwnerRequiresExactKeyAndRetainsBlockedMaterial(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(0, contract.TransportStdio)
	key := candidate.Key()
	secret := []byte("lease-canary")
	lease, err := NewMaterialLease(key, map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: secret})
	require.NoError(t, err)
	secret[0] = 'X'

	_, err = owner.Admit(candidate, lease, nil)
	require.NoError(t, err)
	assert.True(t, lease.Transferred())
	_, err = owner.Admit(candidate, lease, nil)
	assert.ErrorIs(t, err, ErrCandidateOwned)
	material, ok := owner.Material(key, contract.ServerCredentialStatic)
	require.True(t, ok)
	assert.Equal(t, "lease-canary", string(material))

	mismatch := key
	mismatch.Generation++
	assert.False(t, owner.Transition(mismatch, RuntimeNegotiating))
	assert.False(t, owner.Release(mismatch, true))
	assert.True(t, owner.Transition(key, RuntimeNegotiating))
	assert.True(t, owner.Transition(key, RuntimeCataloging))
	assert.True(t, owner.Transition(key, RuntimeActive))
	assert.False(t, owner.Release(key, false))
	phase, ok := owner.Phase(key)
	require.True(t, ok)
	assert.Equal(t, RuntimeBlockedStop, phase)
	retained, ok := owner.Material(key, contract.ServerCredentialStatic)
	require.True(t, ok)
	assert.Equal(t, "lease-canary", string(retained))

	assert.True(t, owner.Release(key, true))
	assert.Equal(t, make([]byte, len(retained)), retained)
	assert.False(t, owner.Release(key, true))
	_, ok = owner.Phase(key)
	assert.False(t, ok)
}

func TestMaterialLeaseTransferAndClearAreOneTimeAndCandidateBound(t *testing.T) {
	key := ownerCandidate(0, contract.TransportStdio).Key()
	lease, err := NewMaterialLease(key, map[contract.ServerCredentialKind][]byte{
		contract.ServerCredentialStatic:      []byte("static"),
		contract.ServerCredentialOAuthClient: []byte("client"),
	})
	require.NoError(t, err)
	mismatch := key
	mismatch.RuntimeID = "other"
	_, ok := lease.transfer(mismatch)
	assert.False(t, ok)
	bundle, ok := lease.transfer(key)
	require.True(t, ok)
	assert.True(t, lease.Transferred())
	_, ok = lease.transfer(key)
	assert.False(t, ok)
	assert.False(t, lease.Clear())
	static, ok := bundle.material(contract.ServerCredentialStatic)
	require.True(t, ok)
	assert.Equal(t, "static", string(static))
	assert.True(t, bundle.clear())
	assert.Equal(t, make([]byte, len(static)), static)
	assert.False(t, bundle.clear())

	fresh, err := NewMaterialLease(key, map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: []byte("fresh")})
	require.NoError(t, err)
	assert.True(t, fresh.Clear())
	assert.False(t, fresh.Clear())
	_, ok = fresh.transfer(key)
	assert.False(t, ok)
}
