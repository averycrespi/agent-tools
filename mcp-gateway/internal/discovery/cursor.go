package discovery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const (
	discoveryCursorPrefix = "mgw_dc1_"
	cursorVersion         = 1
	cursorKeyBytes        = 32
	cursorIDBytes         = 26
	cursorTagBytes        = sha256.Size
	cursorPayloadBytes    = 129
	cursorFrameBytes      = cursorPayloadBytes + cursorTagBytes
	cursorMACDomain       = "mcp-gateway/discovery-cursor/v1\x00"
	nanosecondsPerSecond  = 1_000_000_000

	offsetVersion            = 0
	offsetMethod             = 1
	offsetVisibility         = 2
	offsetPrincipalID        = 3
	offsetCredentialID       = offsetPrincipalID + cursorIDBytes
	offsetProcessGeneration  = offsetCredentialID + cursorIDBytes
	offsetPrincipalRevision  = offsetProcessGeneration + cursorIDBytes
	offsetCredentialRevision = offsetPrincipalRevision + 8
	offsetAuthorization      = offsetCredentialRevision + 8
	offsetEvaluationSeconds  = offsetAuthorization + 8
	offsetEvaluationNanos    = offsetEvaluationSeconds + 8
	offsetActiveGeneration   = offsetEvaluationNanos + 4
	offsetPosition           = offsetActiveGeneration + 8
)

var (
	ErrMalformedCursor    = errors.New("discovery cursor syntax is malformed")
	ErrInvalidCursorState = errors.New("discovery cursor state is invalid")

	cursorMaximumBytes    = int(mustDiscoveryLimit("cursor_bytes"))
	maximumCursorPosition = mustDiscoveryLimit("discoverable_tools")
)

type CursorMethod uint8

const CursorMethodToolsList CursorMethod = 1

type CursorState struct {
	Snapshot Snapshot
	Method   CursorMethod
	Position uint32
}

type CursorCodec struct {
	key [cursorKeyBytes]byte
}

func NewCursorCodec(entropy io.Reader) (*CursorCodec, error) {
	if entropy == nil {
		return nil, fmt.Errorf("discovery cursor entropy is unavailable")
	}
	codec := &CursorCodec{}
	if _, err := io.ReadFull(entropy, codec.key[:]); err != nil {
		return nil, fmt.Errorf("generate discovery cursor key: %w", err)
	}
	return codec, nil
}

func (codec *CursorCodec) Encode(state CursorState) (string, error) {
	if codec == nil || !validCursorState(state) {
		return "", ErrInvalidCursorState
	}
	principalRevision, _ := parseCursorRevision(state.Snapshot.PrincipalRevision, 1)
	credentialRevision, _ := parseCursorRevision(state.Snapshot.CredentialRevision, 1)
	authorizationRevision, _ := parseCursorRevision(state.Snapshot.AuthorizationRevision, 0)
	frame := make([]byte, cursorFrameBytes)
	frame[offsetVersion] = cursorVersion
	frame[offsetMethod] = byte(state.Method)
	frame[offsetVisibility] = encodeCursorVisibility(state.Snapshot.Visibility)
	copy(frame[offsetPrincipalID:offsetCredentialID], state.Snapshot.PrincipalID)
	copy(frame[offsetCredentialID:offsetProcessGeneration], state.Snapshot.CredentialID)
	copy(frame[offsetProcessGeneration:offsetPrincipalRevision], state.Snapshot.Generation.ProcessGeneration)
	binary.BigEndian.PutUint64(frame[offsetPrincipalRevision:offsetCredentialRevision], principalRevision)
	binary.BigEndian.PutUint64(frame[offsetCredentialRevision:offsetAuthorization], credentialRevision)
	binary.BigEndian.PutUint64(frame[offsetAuthorization:offsetEvaluationSeconds], authorizationRevision)
	binary.BigEndian.PutUint64(frame[offsetEvaluationSeconds:offsetEvaluationNanos], encodeCursorSeconds(state.Snapshot.EvaluatedAt.Unix()))
	// time.Time guarantees nanoseconds are in [0, 1e9), which fits uint32.
	binary.BigEndian.PutUint32(frame[offsetEvaluationNanos:offsetActiveGeneration], uint32(state.Snapshot.EvaluatedAt.Nanosecond())) //nolint:gosec
	binary.BigEndian.PutUint64(frame[offsetActiveGeneration:offsetPosition], state.Snapshot.Generation.ActiveGeneration)
	binary.BigEndian.PutUint32(frame[offsetPosition:cursorPayloadBytes], state.Position)
	copy(frame[cursorPayloadBytes:], codec.authenticate(frame[:cursorPayloadBytes]))
	encoded := discoveryCursorPrefix + base64.RawURLEncoding.EncodeToString(frame)
	if len(encoded) > cursorMaximumBytes {
		return "", ErrInvalidCursorState
	}
	return encoded, nil
}

func (codec *CursorCodec) Decode(value string, expectedMethod CursorMethod) (CursorState, error) {
	if codec == nil {
		return CursorState{}, ErrInvalidCursorState
	}
	if value == "" || len(value) > cursorMaximumBytes || !strings.HasPrefix(value, discoveryCursorPrefix) {
		return CursorState{}, ErrMalformedCursor
	}
	encoded := strings.TrimPrefix(value, discoveryCursorPrefix)
	frame, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(frame) != cursorFrameBytes || base64.RawURLEncoding.EncodeToString(frame) != encoded {
		return CursorState{}, ErrMalformedCursor
	}
	expectedTag := codec.authenticate(frame[:cursorPayloadBytes])
	if !hmac.Equal(frame[cursorPayloadBytes:], expectedTag) {
		return CursorState{}, ErrStaleCursor
	}
	if frame[offsetVersion] != cursorVersion || binary.BigEndian.Uint32(frame[offsetEvaluationNanos:offsetActiveGeneration]) >= nanosecondsPerSecond {
		return CursorState{}, ErrStaleCursor
	}
	state := decodeCursorState(frame[:cursorPayloadBytes])
	if !validCursorState(state) || state.Method != expectedMethod {
		return CursorState{}, ErrStaleCursor
	}
	return state, nil
}

func (codec *CursorCodec) authenticate(payload []byte) []byte {
	mac := hmac.New(sha256.New, codec.key[:])
	_, _ = mac.Write([]byte(cursorMACDomain))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func decodeCursorState(payload []byte) CursorState {
	principalRevision := binary.BigEndian.Uint64(payload[offsetPrincipalRevision:offsetCredentialRevision])
	credentialRevision := binary.BigEndian.Uint64(payload[offsetCredentialRevision:offsetAuthorization])
	authorizationRevision := binary.BigEndian.Uint64(payload[offsetAuthorization:offsetEvaluationSeconds])
	seconds := decodeCursorSeconds(binary.BigEndian.Uint64(payload[offsetEvaluationSeconds:offsetEvaluationNanos]))
	nanoseconds := binary.BigEndian.Uint32(payload[offsetEvaluationNanos:offsetActiveGeneration])
	return CursorState{
		Snapshot: Snapshot{
			Generation: catalog.CurrentGeneration{
				ProcessGeneration: string(payload[offsetProcessGeneration:offsetPrincipalRevision]),
				ActiveGeneration:  binary.BigEndian.Uint64(payload[offsetActiveGeneration:offsetPosition]),
			},
			PrincipalID:           string(payload[offsetPrincipalID:offsetCredentialID]),
			PrincipalRevision:     formatCursorRevision(principalRevision),
			Visibility:            decodeCursorVisibility(payload[offsetVisibility]),
			CredentialID:          string(payload[offsetCredentialID:offsetProcessGeneration]),
			CredentialRevision:    formatCursorRevision(credentialRevision),
			AuthorizationRevision: formatCursorRevision(authorizationRevision),
			EvaluatedAt:           time.Unix(seconds, int64(nanoseconds)).UTC(),
		},
		Method:   CursorMethod(payload[offsetMethod]),
		Position: binary.BigEndian.Uint32(payload[offsetPosition:cursorPayloadBytes]),
	}
}

func validCursorState(state CursorState) bool {
	if state.Method != CursorMethodToolsList || !validCursorID(state.Snapshot.PrincipalID) ||
		!validCursorID(state.Snapshot.CredentialID) || !validCursorID(state.Snapshot.Generation.ProcessGeneration) ||
		state.Position == 0 || int64(state.Position) > maximumCursorPosition || !validCursorTime(state.Snapshot.EvaluatedAt) {
		return false
	}
	if _, ok := parseCursorRevision(state.Snapshot.PrincipalRevision, 1); !ok {
		return false
	}
	if _, ok := parseCursorRevision(state.Snapshot.CredentialRevision, 1); !ok {
		return false
	}
	if _, ok := parseCursorRevision(state.Snapshot.AuthorizationRevision, 0); !ok {
		return false
	}
	return encodeCursorVisibility(state.Snapshot.Visibility) != 0
}

func validCursorID(value string) bool {
	if len(value) != cursorIDBytes || value[0] < '0' || value[0] > '7' {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", character) {
			return false
		}
	}
	return true
}

func parseCursorRevision(value string, minimum uint64) (uint64, bool) {
	if value == "" || len(value) > 1 && value[0] == '0' {
		return 0, false
	}
	revision, err := strconv.ParseUint(value, 10, 63)
	return revision, err == nil && revision >= minimum
}

func formatCursorRevision(value uint64) string {
	if value > math.MaxInt64 {
		return ""
	}
	return strconv.FormatUint(value, 10)
}

func encodeCursorSeconds(value int64) uint64 {
	// XORing the sign bit maps every signed second losslessly into an ordered uint64.
	return uint64(value) ^ 1<<63 //nolint:gosec
}

func decodeCursorSeconds(value uint64) int64 {
	// This exactly reverses encodeCursorSeconds; all bit patterns are representable.
	return int64(value ^ 1<<63) //nolint:gosec
}

func validCursorTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Year() >= 1 && value.Year() <= 9999
}

func encodeCursorVisibility(visibility contract.PrincipalVisibility) byte {
	switch visibility {
	case contract.VisibilityRequestable:
		return 1
	case contract.VisibilityAllowedOnly:
		return 2
	case contract.VisibilityAll:
		return 3
	default:
		return 0
	}
}

func decodeCursorVisibility(value byte) contract.PrincipalVisibility {
	switch value {
	case 1:
		return contract.VisibilityRequestable
	case 2:
		return contract.VisibilityAllowedOnly
	case 3:
		return contract.VisibilityAll
	default:
		return ""
	}
}

func mustDiscoveryLimit(name string) int64 {
	limit, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing discovery limit: " + name)
	}
	return limit.Maximum
}
