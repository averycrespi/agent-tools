package discovery

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorCodecRoundTripsEveryBoundFieldWithinLimit(t *testing.T) {
	entropy := testutil.NewFakeEntropy(bytes.Repeat([]byte{0x5a}, 40))
	codec, err := NewCursorCodec(entropy)
	require.NoError(t, err)
	assert.Equal(t, 8, entropy.Remaining())
	state := testCursorState()
	cursor, err := codec.Encode(state)
	require.NoError(t, err)
	assert.Equal(t, len(discoveryCursorPrefix)+base64.RawURLEncoding.EncodedLen(cursorFrameBytes), len(cursor))
	assert.LessOrEqual(t, len(cursor), cursorMaximumBytes)
	assert.True(t, strings.HasPrefix(cursor, discoveryCursorPrefix))

	decoded, err := codec.Decode(cursor, CursorMethodToolsList)
	require.NoError(t, err)
	assert.Equal(t, state, decoded)
	assert.Equal(t, int64(2048), maximumCursorPosition)
}

func TestCursorCodecRejectsEntropyFailures(t *testing.T) {
	for name, reader := range map[string]io.Reader{
		"nil":   nil,
		"empty": bytes.NewReader(nil),
		"short": bytes.NewReader(make([]byte, cursorKeyBytes-1)),
		"error": failingEntropy{},
	} {
		t.Run(name, func(t *testing.T) {
			codec, err := NewCursorCodec(reader)
			assert.Error(t, err)
			assert.Nil(t, codec)
		})
	}
}

func TestCursorCodecDistinguishesMalformedSyntaxFromAuthenticatedStaleness(t *testing.T) {
	codec := mustCursorCodec(t, 1)
	valid, err := codec.Encode(testCursorState())
	require.NoError(t, err)
	encoded := strings.TrimPrefix(valid, discoveryCursorPrefix)
	frame, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)

	noncanonical := []byte(encoded)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, noncanonical[len(noncanonical)-1])
	require.GreaterOrEqual(t, last, 0)
	noncanonical[len(noncanonical)-1] = alphabet[last|1]
	malformed := []string{
		"", "control-cursor", discoveryCursorPrefix + "=", discoveryCursorPrefix + "***",
		discoveryCursorPrefix + base64.RawURLEncoding.EncodeToString(frame[:len(frame)-1]),
		discoveryCursorPrefix + string(noncanonical),
		discoveryCursorPrefix + strings.Repeat("A", cursorMaximumBytes-len(discoveryCursorPrefix)),
		discoveryCursorPrefix + strings.Repeat("A", cursorMaximumBytes+1-len(discoveryCursorPrefix)),
	}
	for _, cursor := range malformed {
		_, err := codec.Decode(cursor, CursorMethodToolsList)
		assert.ErrorIs(t, err, ErrMalformedCursor, cursor)
		assert.NotErrorIs(t, err, ErrStaleCursor, cursor)
	}

	randomFrame := make([]byte, len(frame))
	randomFrame[0] = cursorVersion
	random := discoveryCursorPrefix + base64.RawURLEncoding.EncodeToString(randomFrame)
	_, err = codec.Decode(random, CursorMethodToolsList)
	assert.ErrorIs(t, err, ErrStaleCursor)
	assert.NotErrorIs(t, err, ErrMalformedCursor)

	for name, mutate := range map[string]func([]byte){
		"version":    func(value []byte) { value[offsetVersion]++ },
		"visibility": func(value []byte) { value[offsetVisibility] = 0xff },
		"nanoseconds": func(value []byte) {
			binary.BigEndian.PutUint32(value[offsetEvaluationNanos:offsetActiveGeneration], nanosecondsPerSecond)
		},
	} {
		t.Run("authenticated "+name, func(t *testing.T) {
			mutated := append([]byte(nil), frame...)
			mutate(mutated)
			copy(mutated[cursorPayloadBytes:], codec.authenticate(mutated[:cursorPayloadBytes]))
			value := discoveryCursorPrefix + base64.RawURLEncoding.EncodeToString(mutated)
			_, decodeErr := codec.Decode(value, CursorMethodToolsList)
			assert.ErrorIs(t, decodeErr, ErrStaleCursor)
		})
	}
}

func TestCursorCodecEveryPayloadAndTagMutationIsStale(t *testing.T) {
	codec := mustCursorCodec(t, 2)
	cursor, err := codec.Encode(testCursorState())
	require.NoError(t, err)
	frame, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, discoveryCursorPrefix))
	require.NoError(t, err)
	for index := range frame {
		mutated := append([]byte(nil), frame...)
		mutated[index] ^= 0x01
		value := discoveryCursorPrefix + base64.RawURLEncoding.EncodeToString(mutated)
		_, err := codec.Decode(value, CursorMethodToolsList)
		assert.ErrorIs(t, err, ErrStaleCursor, "byte %d", index)
	}
}

func TestCursorCodecRejectsCrossMethodRestartAndSnapshotBoundaries(t *testing.T) {
	codec := mustCursorCodec(t, 3)
	state := testCursorState()
	cursor, err := codec.Encode(state)
	require.NoError(t, err)
	_, err = codec.Decode(cursor, CursorMethod(2))
	assert.ErrorIs(t, err, ErrStaleCursor)
	_, err = mustCursorCodec(t, 4).Decode(cursor, CursorMethodToolsList)
	assert.ErrorIs(t, err, ErrStaleCursor)

	decoded, err := codec.Decode(cursor, CursorMethodToolsList)
	require.NoError(t, err)
	policy := &fakePolicySource{view: policyView(decoded.Snapshot.Visibility, nil)}
	policy.view.Binding.PrincipalID = decoded.Snapshot.PrincipalID
	policy.view.Binding.PrincipalRevision = decoded.Snapshot.PrincipalRevision
	policy.view.Binding.CredentialID = decoded.Snapshot.CredentialID
	policy.view.Binding.CredentialRevision = decoded.Snapshot.CredentialRevision
	policy.view.AuthorizationRevision = decoded.Snapshot.AuthorizationRevision
	catalogs := &fakeCatalogSource{snapshot: currentSnapshot(nil), current: true}
	catalogs.snapshot.Generation = decoded.Snapshot.Generation
	service := mustService(t, policy, catalogs)
	for name, change := range map[string]func(){
		"principal":              func() { policy.view.Binding.PrincipalID = opaqueID(10) },
		"principal revision":     func() { policy.view.Binding.PrincipalRevision = "8" },
		"visibility":             func() { policy.view.Binding.Visibility = contract.VisibilityAllowedOnly },
		"credential":             func() { policy.view.Binding.CredentialID = opaqueID(11) },
		"credential revision":    func() { policy.view.Binding.CredentialRevision = "9" },
		"authorization revision": func() { policy.view.AuthorizationRevision = "10" },
		"process generation":     func() { catalogs.snapshot.Generation.ProcessGeneration = opaqueID(12) },
		"active generation":      func() { catalogs.snapshot.Generation.ActiveGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			originalPolicy := policy.view
			originalGeneration := catalogs.snapshot.Generation
			change()
			_, projectErr := service.Project(t.Context(), Request{Continuation: &decoded.Snapshot})
			assert.ErrorIs(t, projectErr, ErrStaleCursor)
			policy.view = originalPolicy
			catalogs.snapshot.Generation = originalGeneration
		})
	}
}

func TestCursorCodecPreservesPinnedNanosecondsAndValidatesState(t *testing.T) {
	codec := mustCursorCodec(t, 5)
	state := testCursorState()
	state.Snapshot.EvaluatedAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	cursor, err := codec.Encode(state)
	require.NoError(t, err)
	decoded, err := codec.Decode(cursor, CursorMethodToolsList)
	require.NoError(t, err)
	assert.True(t, state.Snapshot.EvaluatedAt.Equal(decoded.Snapshot.EvaluatedAt))
	state.Snapshot.PrincipalRevision = "9223372036854775807"
	state.Snapshot.CredentialRevision = "9223372036854775807"
	state.Snapshot.AuthorizationRevision = "9223372036854775807"
	state.Snapshot.Generation.ActiveGeneration = ^uint64(0)
	cursor, err = codec.Encode(state)
	require.NoError(t, err)
	decoded, err = codec.Decode(cursor, CursorMethodToolsList)
	require.NoError(t, err)
	assert.Equal(t, state, decoded)

	invalid := []CursorState{state, state, state, state, state, state, state}
	invalid[0].Position = 0
	invalid[1].Position = uint32(maximumCursorPosition + 1)
	invalid[2].Snapshot.PrincipalID = "not-an-id"
	invalid[3].Snapshot.PrincipalRevision = "01"
	invalid[4].Snapshot.CredentialRevision = "0"
	invalid[5].Snapshot.AuthorizationRevision = "-1"
	invalid[6].Snapshot.EvaluatedAt = time.Time{}
	for _, value := range invalid {
		_, err := codec.Encode(value)
		assert.ErrorIs(t, err, ErrInvalidCursorState)
	}
}

func TestCursorCodecContainsNoSecretsAndUsesConstantTimeMAC(t *testing.T) {
	keyCanary := "cursor-key-material-canary-12345"
	codec, err := NewCursorCodec(strings.NewReader(keyCanary))
	require.NoError(t, err)
	cursor, err := codec.Encode(testCursorState())
	require.NoError(t, err)
	frame, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, discoveryCursorPrefix))
	require.NoError(t, err)
	for _, canary := range []string{
		keyCanary, "agent_v1_raw-bearer-canary", "verifier-canary", "fingerprint-canary", "constraint-value-canary", "grant-id-canary",
	} {
		assert.NotContains(t, cursor, canary)
		assert.NotContains(t, string(frame), canary)
	}
	source, err := os.ReadFile("cursor.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), "hmac.Equal(")
	assert.NotContains(t, string(source), "bytes.Equal(")
}

func testCursorState() CursorState {
	return CursorState{
		Snapshot: Snapshot{
			Generation:  catalog.CurrentGeneration{ProcessGeneration: opaqueID(3), ActiveGeneration: 17},
			PrincipalID: opaqueID(1), PrincipalRevision: "3", Visibility: contract.VisibilityRequestable,
			CredentialID: opaqueID(2), CredentialRevision: "4", AuthorizationRevision: "7",
			EvaluatedAt: time.Date(2026, 8, 25, 18, 0, 0, 123456789, time.UTC),
		},
		Method: CursorMethodToolsList, Position: 2048,
	}
}

func mustCursorCodec(t *testing.T, fill byte) *CursorCodec {
	t.Helper()
	codec, err := NewCursorCodec(bytes.NewReader(bytes.Repeat([]byte{fill}, cursorKeyBytes)))
	require.NoError(t, err)
	return codec
}

type failingEntropy struct{}

func (failingEntropy) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
