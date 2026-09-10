package invocation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

const invocationCursorVersion = 2

type invocationCursor struct {
	Version       int    `json:"v"`
	Epoch         string `json:"e"`
	MAC           string `json:"m,omitempty"`
	QueryDigest   string `json:"q"`
	NamesDigest   string `json:"p"`
	UpperSequence int64  `json:"u"`
	NextSequence  int64  `json:"n"`
}

func (repository *Repository) encodeInvocationCursor(binding contract.InvocationCursorBinding) (string, error) {
	if !validInvocationFilters(binding.Filters) || binding.UpperSequence <= 0 || binding.NextSequence <= 0 || binding.NextSequence > binding.UpperSequence {
		return "", ErrInvalidCursor
	}
	namesDigest := binding.NamesDigest
	if namesDigest == "" {
		namesDigest = searchDigest(map[string]string{})
	}
	value := invocationCursor{
		Version: invocationCursorVersion, Epoch: repository.cursorEpoch(), QueryDigest: searchDigest(binding.Filters), NamesDigest: namesDigest,
		UpperSequence: binding.UpperSequence, NextSequence: binding.NextSequence,
	}
	value.MAC = repository.cursorMAC(value)
	contents, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidCursor
	}
	encoded := base64.RawURLEncoding.EncodeToString(contents)
	if int64(len(encoded)) > invocationCursorLimit() {
		return "", ErrInvalidCursor
	}
	return encoded, nil
}

func (repository *Repository) decodeInvocationCursor(value string) (contract.InvocationCursorBinding, error) {
	if value == "" || int64(len(value)) > invocationCursorLimit() {
		return contract.InvocationCursorBinding{}, ErrInvalidCursor
	}
	contents, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(contents) != value {
		return contract.InvocationCursorBinding{}, ErrInvalidCursor
	}
	var decoded invocationCursor
	if strictjson.Decode(contents, &decoded, strictjson.Options{MaxBytes: invocationCursorLimit(), MaxDepth: jsonDepthLimit(), RejectUnknownMembers: true}) != nil ||
		decoded.UpperSequence <= 0 || decoded.NextSequence <= 0 || decoded.NextSequence > decoded.UpperSequence {
		return contract.InvocationCursorBinding{}, ErrInvalidCursor
	}
	if decoded.Version != invocationCursorVersion || decoded.Epoch != repository.cursorEpoch() {
		return contract.InvocationCursorBinding{}, ErrStaleCursor
	}
	if !validSearchDigest(decoded.QueryDigest) || !validSearchDigest(decoded.NamesDigest) || !hmac.Equal([]byte(decoded.MAC), []byte(repository.cursorMAC(decoded))) {
		return contract.InvocationCursorBinding{}, ErrInvalidCursor
	}
	return contract.InvocationCursorBinding{QueryDigest: decoded.QueryDigest, NamesDigest: decoded.NamesDigest, UpperSequence: decoded.UpperSequence, NextSequence: decoded.NextSequence}, nil
}
func (repository *Repository) cursorEpoch() string {
	digest := sha256.Sum256(repository.cursorKey[:])
	return hex.EncodeToString(digest[:8])
}

func (repository *Repository) cursorMAC(value invocationCursor) string {
	value.MAC = ""
	contents, _ := json.Marshal(value)
	mac := hmac.New(sha256.New, repository.cursorKey[:])
	_, _ = mac.Write(contents)
	return hex.EncodeToString(mac.Sum(nil))
}

func validSearchDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}
