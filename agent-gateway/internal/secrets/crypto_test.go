package secrets

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWrapUnwrapDEK_Roundtrip verifies that a DEK wrapped with a KEK can be
// unwrapped back to the original bytes.
func TestWrapUnwrapDEK_Roundtrip(t *testing.T) {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	dek, err := generateKey()
	require.NoError(t, err)

	ct, nonce, err := wrapDEK(kek, dek)
	require.NoError(t, err)
	require.Len(t, nonce, 12)
	// GCM ciphertext is plaintext + 16-byte tag.
	require.Len(t, ct, len(dek)+16)

	got, err := unwrapDEK(kek, ct, nonce)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(dek, got), "unwrapped DEK must equal original")
}

// TestUnwrapDEK_WrongKEK verifies that unwrapping with a different KEK fails
// (GCM tag verification rejects).
func TestUnwrapDEK_WrongKEK(t *testing.T) {
	kek1 := make([]byte, 32)
	for i := range kek1 {
		kek1[i] = byte(i + 1)
	}
	kek2 := make([]byte, 32)
	for i := range kek2 {
		kek2[i] = byte(i + 100)
	}
	dek, err := generateKey()
	require.NoError(t, err)

	ct, nonce, err := wrapDEK(kek1, dek)
	require.NoError(t, err)

	_, err = unwrapDEK(kek2, ct, nonce)
	assert.Error(t, err, "unwrap with wrong KEK must fail")
}

// TestUnwrapDEK_TamperedCiphertext verifies that flipping a bit in the
// ciphertext causes unwrap to fail (GCM authentication).
func TestUnwrapDEK_TamperedCiphertext(t *testing.T) {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	dek, err := generateKey()
	require.NoError(t, err)

	ct, nonce, err := wrapDEK(kek, dek)
	require.NoError(t, err)

	// Flip one bit of the first ciphertext byte.
	ct[0] ^= 0x01

	_, err = unwrapDEK(kek, ct, nonce)
	assert.Error(t, err, "unwrap with tampered ciphertext must fail")
}

// TestDeriveKEK_Deterministic verifies that HKDF with the same master key and
// salt produces the same KEK.
func TestDeriveKEK_Deterministic(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i + 50)
	}

	k1, err := deriveKEK(master, salt)
	require.NoError(t, err)
	k2, err := deriveKEK(master, salt)
	require.NoError(t, err)

	require.Len(t, k1, 32)
	assert.True(t, bytes.Equal(k1, k2), "deriveKEK must be deterministic")
}

// TestDeriveKEK_SaltSeparates verifies that different salts produce different
// KEKs from the same master key.
func TestDeriveKEK_SaltSeparates(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	salt1 := bytes.Repeat([]byte{0x01}, 16)
	salt2 := bytes.Repeat([]byte{0x02}, 16)

	k1, err := deriveKEK(master, salt1)
	require.NoError(t, err)
	k2, err := deriveKEK(master, salt2)
	require.NoError(t, err)

	assert.False(t, bytes.Equal(k1, k2), "different salts must produce different KEKs")
}

// TestDeriveKEK_MasterSeparates verifies that different master keys produce
// different KEKs for the same salt.
func TestDeriveKEK_MasterSeparates(t *testing.T) {
	salt := bytes.Repeat([]byte{0x01}, 16)
	m1 := bytes.Repeat([]byte{0xAA}, 32)
	m2 := bytes.Repeat([]byte{0xBB}, 32)

	k1, err := deriveKEK(m1, salt)
	require.NoError(t, err)
	k2, err := deriveKEK(m2, salt)
	require.NoError(t, err)

	assert.False(t, bytes.Equal(k1, k2), "different master keys must produce different KEKs")
}

// TestEncryptDecryptRow_Roundtrip verifies that a row encrypted with a given
// (name, scope) pair decrypts back to the original plaintext when the same
// pair is supplied as AAD on decrypt.
func TestEncryptDecryptRow_Roundtrip(t *testing.T) {
	dek, err := generateKey()
	require.NoError(t, err)
	plaintext := []byte("super-secret-token-value")

	ct, nonce, err := encryptRow(dek, "github_token", "agent:mybot", plaintext)
	require.NoError(t, err)
	require.Len(t, nonce, 12)
	// GCM ciphertext is plaintext + 16-byte tag.
	require.Len(t, ct, len(plaintext)+16)

	got, err := decryptRow(dek, "github_token", "agent:mybot", ct, nonce)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(plaintext, got), "decrypted plaintext must equal original")
}

// TestEncryptRow_FreshNonce verifies that two encryptions of the same
// (dek, name, scope, plaintext) produce different ciphertexts and nonces —
// a DB-read attacker cannot confirm that two rows hold the same value.
func TestEncryptRow_FreshNonce(t *testing.T) {
	dek, err := generateKey()
	require.NoError(t, err)
	plaintext := []byte("same-value")

	ct1, nonce1, err := encryptRow(dek, "n", "global", plaintext)
	require.NoError(t, err)
	ct2, nonce2, err := encryptRow(dek, "n", "global", plaintext)
	require.NoError(t, err)

	assert.False(t, bytes.Equal(nonce1, nonce2), "nonces must be fresh per call")
	assert.False(t, bytes.Equal(ct1, ct2), "ciphertexts must differ under fresh nonce")
}

// TestDecryptRow_SwapAttack verifies that ciphertext produced with one
// (name, scope) cannot be decrypted under a different (name, scope). This
// blocks the "DB-write-capable attacker copies ciphertext from row A into
// row B to inject the wrong credential" attack: GCM authentication over AAD
// rejects the swap.
func TestDecryptRow_SwapAttack(t *testing.T) {
	dek, err := generateKey()
	require.NoError(t, err)
	plaintext := []byte("prod-api-key")

	ct, nonce, err := encryptRow(dek, "api_key", "agent:prodbot", plaintext)
	require.NoError(t, err)

	// Different name, same scope.
	_, err = decryptRow(dek, "other_key", "agent:prodbot", ct, nonce)
	assert.Error(t, err, "decrypt with different name must fail")

	// Same name, different scope.
	_, err = decryptRow(dek, "api_key", "global", ct, nonce)
	assert.Error(t, err, "decrypt with different scope must fail")

	// Both different.
	_, err = decryptRow(dek, "other_key", "global", ct, nonce)
	assert.Error(t, err, "decrypt with different name and scope must fail")
}

// TestDecryptRow_DelimiterCollision verifies that the \x00 delimiter in AAD
// prevents canonical-form collisions: ("ax", "") and ("a", "x") would both
// concatenate to "ax" without a delimiter, so an attacker could swap rows
// whose name||scope happen to be the same string. The delimiter distinguishes
// them — "ax\x00" vs "a\x00x" — so GCM authentication rejects the swap.
func TestDecryptRow_DelimiterCollision(t *testing.T) {
	dek, err := generateKey()
	require.NoError(t, err)
	plaintext := []byte("value")

	ct, nonce, err := encryptRow(dek, "a", "x", plaintext)
	require.NoError(t, err)

	_, err = decryptRow(dek, "ax", "", ct, nonce)
	assert.Error(t, err, "decrypt with canonical-collision (name,scope) must fail")
}

// TestDecryptRow_WrongDEK verifies that a ciphertext produced under one DEK
// cannot be decrypted under another — GCM authentication fails.
func TestDecryptRow_WrongDEK(t *testing.T) {
	dek1, err := generateKey()
	require.NoError(t, err)
	dek2, err := generateKey()
	require.NoError(t, err)

	ct, nonce, err := encryptRow(dek1, "n", "global", []byte("v"))
	require.NoError(t, err)

	_, err = decryptRow(dek2, "n", "global", ct, nonce)
	assert.Error(t, err, "decrypt with wrong DEK must fail")
}

// TestDecryptRow_TamperedCiphertext verifies that flipping a bit of the
// ciphertext causes decrypt to fail (GCM authentication).
func TestDecryptRow_TamperedCiphertext(t *testing.T) {
	dek, err := generateKey()
	require.NoError(t, err)

	ct, nonce, err := encryptRow(dek, "n", "global", []byte("v"))
	require.NoError(t, err)

	ct[0] ^= 0x01

	_, err = decryptRow(dek, "n", "global", ct, nonce)
	assert.Error(t, err, "decrypt with tampered ciphertext must fail")
}
