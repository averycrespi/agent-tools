package grants

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Credential is a freshly minted (id, token, token_hash) triple. The raw
// Token is shown to the operator exactly once at creation; only TokenHash
// is persisted.
type Credential struct {
	ID        string
	Token     string
	TokenHash string
}

// NewCredential mints a fresh grant credential. The ID is a short
// human-friendly handle safe to log; the Token is the secret presented
// on requests as X-Grant-Token.
func NewCredential() (Credential, error) {
	idBytes := make([]byte, 6) // 12 hex chars
	if _, err := rand.Read(idBytes); err != nil {
		return Credential{}, fmt.Errorf("generating grant id: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Credential{}, fmt.Errorf("generating grant token: %w", err)
	}
	token := "gr_" + hex.EncodeToString(tokenBytes)
	return Credential{
		ID:        "grt_" + hex.EncodeToString(idBytes),
		Token:     token,
		TokenHash: HashToken(token),
	}, nil
}

// HashToken returns the hex-encoded SHA-256 of the given raw token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
