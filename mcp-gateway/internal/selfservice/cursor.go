package selfservice

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

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
)

const (
	cursorPrefix       = "mgw_sc1_"
	cursorVersion      = byte(1)
	cursorKeyBytes     = 32
	cursorIDBytes      = 26
	cursorTagBytes     = sha256.Size
	cursorPayloadBytes = 139
	cursorFrameBytes   = cursorPayloadBytes + cursorTagBytes

	requestCursorDomain = "mcp-gateway/selfservice/list-grant-requests-cursor/v1\x00"
	grantCursorDomain   = "mcp-gateway/selfservice/list-grants-cursor/v1\x00"

	cursorMethodRequests = byte(1)
	cursorMethodGrants   = byte(2)
	maximumRequestFilter = byte(4)

	offsetCursorVersion            = 0
	offsetCursorMethod             = 1
	offsetCursorFilter             = 2
	offsetCursorPrincipalID        = 3
	offsetCursorCredentialID       = offsetCursorPrincipalID + cursorIDBytes
	offsetCursorProcessGeneration  = offsetCursorCredentialID + cursorIDBytes
	offsetCursorPrincipalRevision  = offsetCursorProcessGeneration + cursorIDBytes
	offsetCursorCredentialRevision = offsetCursorPrincipalRevision + 8
	offsetCursorUpper              = offsetCursorCredentialRevision + 8
	offsetCursorAfter              = offsetCursorUpper + 8
	offsetCursorAfterID            = offsetCursorAfter + 8
)

var (
	ErrMalformedCursor    = errors.New("self-service cursor syntax is malformed")
	ErrInvalidCursorState = errors.New("self-service cursor state is invalid")

	cursorMaximumBytes = mustSelfServiceLimit("cursor_bytes")
)

// CursorCodec authenticates method- and process-bound agent cursors.
type CursorCodec struct {
	key               [cursorKeyBytes]byte
	processGeneration string
}

type cursorState struct {
	method             byte
	filter             byte
	principalID        string
	principalRevision  string
	credentialID       string
	credentialRevision string
	processGeneration  string
	upper              int64
	after              int64
	afterID            string
}

// NewCursorCodec creates one process-local cursor authority.
func NewCursorCodec(entropy io.Reader, processGeneration string) (*CursorCodec, error) {
	if entropy == nil || !validCursorID(processGeneration) {
		return nil, errors.New("self-service cursor dependencies are invalid")
	}
	codec := &CursorCodec{processGeneration: processGeneration}
	if _, err := io.ReadFull(entropy, codec.key[:]); err != nil {
		return nil, fmt.Errorf("generate self-service cursor key: %w", err)
	}
	return codec, nil
}

func (codec *CursorCodec) EncodeRequestCursor(subject authorization.AdmittedSubject, state *contract.GrantRequestState, cursor grantrequests.SelfCursor) (string, error) {
	filter, valid := encodeRequestFilter(state)
	if !valid {
		return "", ErrInvalidCursorState
	}
	encoded := cursorStateForSubject(subject, cursorMethodRequests, filter, cursor)
	if codec != nil {
		encoded.processGeneration = codec.processGeneration
	}
	return codec.encode(encoded)
}

func (codec *CursorCodec) DecodeRequestCursor(value string, subject authorization.AdmittedSubject, state *contract.GrantRequestState) (grantrequests.SelfCursor, contract.CursorOutcome, error) {
	filter, valid := encodeRequestFilter(state)
	if !valid {
		return grantrequests.SelfCursor{}, "", ErrInvalidCursorState
	}
	decoded, outcome, err := codec.decode(value, cursorMethodRequests, filter, subject)
	if err != nil || outcome != contract.CursorOK {
		return grantrequests.SelfCursor{}, outcome, err
	}
	return grantrequests.SelfCursor{Upper: decoded.upper, After: decoded.after, AfterID: decoded.afterID}, contract.CursorOK, nil
}

func (codec *CursorCodec) EncodeGrantCursor(subject authorization.AdmittedSubject, cursor authorization.SelfGrantCursor) (string, error) {
	encoded := cursorStateForSubject(subject, cursorMethodGrants, 0, grantrequests.SelfCursor{
		Upper: cursor.Upper, After: cursor.After, AfterID: cursor.AfterID,
	})
	if codec != nil {
		encoded.processGeneration = codec.processGeneration
	}
	return codec.encode(encoded)
}

func (codec *CursorCodec) DecodeGrantCursor(value string, subject authorization.AdmittedSubject) (authorization.SelfGrantCursor, contract.CursorOutcome, error) {
	decoded, outcome, err := codec.decode(value, cursorMethodGrants, 0, subject)
	if err != nil || outcome != contract.CursorOK {
		return authorization.SelfGrantCursor{}, outcome, err
	}
	return authorization.SelfGrantCursor{Upper: decoded.upper, After: decoded.after, AfterID: decoded.afterID}, contract.CursorOK, nil
}

func (codec *CursorCodec) encode(state cursorState) (string, error) {
	if codec == nil || !validCursorState(state) || state.processGeneration != codec.processGeneration {
		return "", ErrInvalidCursorState
	}
	principalRevision, _ := parseCursorRevision(state.principalRevision)
	credentialRevision, _ := parseCursorRevision(state.credentialRevision)
	frame := make([]byte, cursorFrameBytes)
	frame[offsetCursorVersion] = cursorVersion
	frame[offsetCursorMethod] = state.method
	frame[offsetCursorFilter] = state.filter
	copy(frame[offsetCursorPrincipalID:offsetCursorCredentialID], state.principalID)
	copy(frame[offsetCursorCredentialID:offsetCursorProcessGeneration], state.credentialID)
	copy(frame[offsetCursorProcessGeneration:offsetCursorPrincipalRevision], state.processGeneration)
	binary.BigEndian.PutUint64(frame[offsetCursorPrincipalRevision:offsetCursorCredentialRevision], principalRevision)
	binary.BigEndian.PutUint64(frame[offsetCursorCredentialRevision:offsetCursorUpper], credentialRevision)
	binary.BigEndian.PutUint64(frame[offsetCursorUpper:offsetCursorAfter], encodeCursorPosition(state.upper))
	binary.BigEndian.PutUint64(frame[offsetCursorAfter:offsetCursorAfterID], encodeCursorPosition(state.after))
	copy(frame[offsetCursorAfterID:cursorPayloadBytes], state.afterID)
	copy(frame[cursorPayloadBytes:], codec.authenticate(state.method, frame[:cursorPayloadBytes]))
	encoded := cursorPrefix + base64.RawURLEncoding.EncodeToString(frame)
	if int64(len(encoded)) > cursorMaximumBytes {
		return "", ErrInvalidCursorState
	}
	return encoded, nil
}

func ValidateCursorSyntax(value string) error {
	if value == "" || int64(len(value)) > cursorMaximumBytes || !strings.HasPrefix(value, cursorPrefix) {
		return ErrMalformedCursor
	}
	encoded := strings.TrimPrefix(value, cursorPrefix)
	frame, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(frame) != cursorFrameBytes || base64.RawURLEncoding.EncodeToString(frame) != encoded {
		return ErrMalformedCursor
	}
	return nil
}

func (codec *CursorCodec) decode(value string, expectedMethod, expectedFilter byte, subject authorization.AdmittedSubject) (cursorState, contract.CursorOutcome, error) {
	if codec == nil {
		return cursorState{}, "", ErrInvalidCursorState
	}
	if err := ValidateCursorSyntax(value); err != nil {
		return cursorState{}, "", err
	}
	encoded := strings.TrimPrefix(value, cursorPrefix)
	frame, _ := base64.RawURLEncoding.DecodeString(encoded)
	method := frame[offsetCursorMethod]
	if method != cursorMethodRequests && method != cursorMethodGrants {
		return cursorState{}, contract.CursorInvalid, nil
	}
	if !hmac.Equal(frame[cursorPayloadBytes:], codec.authenticate(method, frame[:cursorPayloadBytes])) {
		return cursorState{}, contract.CursorInvalid, nil
	}
	state := decodeCursorState(frame[:cursorPayloadBytes])
	if frame[offsetCursorVersion] != cursorVersion || !validCursorState(state) {
		return cursorState{}, contract.CursorInvalid, nil
	}
	if state.method != expectedMethod || state.filter != expectedFilter || state.principalID != subject.PrincipalID() ||
		state.principalRevision != subject.PrincipalRevision() || state.credentialID != subject.CredentialID() ||
		state.credentialRevision != subject.CredentialRevision() || state.processGeneration != codec.processGeneration {
		return cursorState{}, contract.CursorStale, nil
	}
	return state, contract.CursorOK, nil
}

func (codec *CursorCodec) authenticate(method byte, payload []byte) []byte {
	domain := requestCursorDomain
	if method == cursorMethodGrants {
		domain = grantCursorDomain
	}
	mac := hmac.New(sha256.New, codec.key[:])
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func cursorStateForSubject(subject authorization.AdmittedSubject, method, filter byte, cursor grantrequests.SelfCursor) cursorState {
	return cursorState{
		method: method, filter: filter,
		principalID: subject.PrincipalID(), principalRevision: subject.PrincipalRevision(),
		credentialID: subject.CredentialID(), credentialRevision: subject.CredentialRevision(),
		upper: cursor.Upper, after: cursor.After, afterID: cursor.AfterID,
	}
}

func decodeCursorState(payload []byte) cursorState {
	principalRevision := binary.BigEndian.Uint64(payload[offsetCursorPrincipalRevision:offsetCursorCredentialRevision])
	credentialRevision := binary.BigEndian.Uint64(payload[offsetCursorCredentialRevision:offsetCursorUpper])
	return cursorState{
		method: payload[offsetCursorMethod], filter: payload[offsetCursorFilter],
		principalID:       string(payload[offsetCursorPrincipalID:offsetCursorCredentialID]),
		credentialID:      string(payload[offsetCursorCredentialID:offsetCursorProcessGeneration]),
		processGeneration: string(payload[offsetCursorProcessGeneration:offsetCursorPrincipalRevision]),
		principalRevision: strconv.FormatUint(principalRevision, 10), credentialRevision: strconv.FormatUint(credentialRevision, 10),
		upper:   decodeCursorPosition(binary.BigEndian.Uint64(payload[offsetCursorUpper:offsetCursorAfter])),
		after:   decodeCursorPosition(binary.BigEndian.Uint64(payload[offsetCursorAfter:offsetCursorAfterID])),
		afterID: string(payload[offsetCursorAfterID:cursorPayloadBytes]),
	}
}

func validCursorState(state cursorState) bool {
	if state.method != cursorMethodRequests && state.method != cursorMethodGrants || state.filter > maximumRequestFilter ||
		!validCursorID(state.principalID) || !validCursorID(state.credentialID) || !validCursorID(state.processGeneration) ||
		state.upper < 1 || state.after < 1 || state.after > state.upper || !validCursorID(state.afterID) {
		return false
	}
	if state.method == cursorMethodGrants && state.filter != 0 {
		return false
	}
	_, principalValid := parseCursorRevision(state.principalRevision)
	_, credentialValid := parseCursorRevision(state.credentialRevision)
	return principalValid && credentialValid
}

func encodeCursorPosition(value int64) uint64 {
	// Cursor state validation admits only positive int64 positions.
	return uint64(value) //nolint:gosec
}

func decodeCursorPosition(value uint64) int64 {
	if value > math.MaxInt64 {
		return -1
	}
	return int64(value) //nolint:gosec
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

func parseCursorRevision(value string) (uint64, bool) {
	if value == "" || len(value) > 1 && value[0] == '0' {
		return 0, false
	}
	revision, err := strconv.ParseUint(value, 10, 63)
	return revision, err == nil && revision > 0
}

func encodeRequestFilter(state *contract.GrantRequestState) (byte, bool) {
	if state == nil {
		return 0, true
	}
	for index, candidate := range contract.GrantRequestStates() {
		if *state == candidate {
			return byte(index + 1), true
		}
	}
	return 0, false
}

func mustSelfServiceLimit(name string) int64 {
	limit, found := contract.FixedLimitByName(name)
	if !found {
		panic("missing self-service limit: " + name)
	}
	return limit.Maximum
}
