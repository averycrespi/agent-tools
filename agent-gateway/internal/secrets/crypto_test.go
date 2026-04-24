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
