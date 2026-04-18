// Package agents provides agent identity management with argon2id token auth.
package agents

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const (
	tokenPrefix = "agw_"
	tokenBody   = 32 // random bytes → ~43 base62 chars
	prefixLen   = 12 // "agw_" + 8 chars of body
)

// MintToken generates a new agent token: "agw_" followed by 32 random bytes
// encoded in base62. Total length is 47 characters (agw_ + ~43 base62 chars).
func MintToken() string {
	b := make([]byte, tokenBody)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("agents: crypto/rand.Read: %v", err))
	}

	// Encode as base62 using math/big.
	n := new(big.Int).SetBytes(b)
	body := n.Text(62)

	// Pad with leading zeros if big.Int.Text produces fewer than expected chars.
	// 32 bytes = 256 bits; base62(2^256) has at most 43 chars.
	const maxBase62Len = 43
	for len(body) < maxBase62Len {
		body = "0" + body
	}

	return tokenPrefix + body
}

// Prefix returns the first 12 characters of a token ("agw_" + 8 body chars).
// This is stored in the DB for O(1) lookup during authentication.
func Prefix(tok string) string {
	if len(tok) < prefixLen {
		return tok
	}
	return tok[:prefixLen]
}
