// Package secrets implements encrypted storage for sandbox-injected secrets.
//
// Key hierarchy (three pieces, separated on purpose):
//
//  1. Master key — 32 bytes of high-entropy material held in the OS keychain
//     (file fallback at 0o600). Never touches the database. Rotated via
//     `master-key rotate`; rotation only has to re-wrap the DEK, not re-encrypt
//     every row.
//  2. KEK (key-encryption key) — 32 bytes derived on each store-open by
//     `deriveKEK(masterKey, meta.kek_kdf_salt)` via HKDF-SHA256. Exists only in
//     memory; never persisted. Used solely to unwrap the DEK.
//  3. DEK (data-encryption key) — 32 bytes of random AES-256 key material,
//     generated once at first-open and wrapped by the KEK (ciphertext + nonce
//     stored in `meta`). The DEK is what actually encrypts/decrypts secret rows
//     (AES-256-GCM, per-row nonce and AAD).
//
// The indirection (master → KEK → DEK) is what makes master-key rotation cheap:
// rotating the keychain master rewraps only the ~60-byte DEK blob in `meta`,
// not every row.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const nonceSize = 12

// encrypt encrypts plaintext using AES-256-GCM with a freshly-generated
// 12-byte random nonce. Returns (nonce, ciphertext, error).
func encrypt(key, plaintext []byte) (nonce, ciphertext []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce = make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return nonce, ciphertext, nil
}

// decrypt decrypts a ciphertext encrypted by encrypt.
func decrypt(key, nonce, ciphertext []byte) ([]byte, error) {
	if len(nonce) != nonceSize {
		return nil, errors.New("invalid nonce length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plain, nil
}

// generateKey generates a new 32-byte random AES-256 key.
func generateKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return key, nil
}

// wrapDEK encrypts dek under kek using AES-256-GCM with a fresh random nonce.
// Returns (ciphertext, nonce, error). No AAD is used: the DEK blob is
// single-purpose and never row-keyed, so there is no row/column context to
// bind. kek and dek must both be 32 bytes.
//
// Invariant: every call uses a newly-generated nonce. Consequence: even if the
// same DEK is wrapped twice (e.g. rotating the master key without rotating the
// DEK), the stored ciphertexts differ — attackers observing multiple wraps
// cannot confirm DEK reuse.
func wrapDEK(kek, dek []byte) (ciphertext, nonce []byte, err error) {
	if len(kek) != 32 {
		return nil, nil, errors.New("wrapDEK: kek must be 32 bytes")
	}
	if len(dek) != 32 {
		return nil, nil, errors.New("wrapDEK: dek must be 32 bytes")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, nil, fmt.Errorf("wrapDEK: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("wrapDEK: create GCM: %w", err)
	}
	nonce = make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("wrapDEK: generate nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, dek, nil)
	return ciphertext, nonce, nil
}

// unwrapDEK decrypts a DEK previously produced by wrapDEK. GCM authentication
// ensures tampered ciphertext or a mismatched KEK return an error instead of
// silently yielding wrong key material.
func unwrapDEK(kek, ciphertext, nonce []byte) ([]byte, error) {
	if len(kek) != 32 {
		return nil, errors.New("unwrapDEK: kek must be 32 bytes")
	}
	if len(nonce) != nonceSize {
		return nil, errors.New("unwrapDEK: invalid nonce length")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("unwrapDEK: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("unwrapDEK: create GCM: %w", err)
	}
	dek, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrapDEK: decrypt: %w", err)
	}
	if len(dek) != 32 {
		return nil, fmt.Errorf("unwrapDEK: unwrapped key has wrong length %d", len(dek))
	}
	return dek, nil
}

// rowAAD builds the additional-authenticated-data blob that binds a row's
// ciphertext to its (name, scope) identity. The \x00 delimiter prevents
// canonical-form collisions: ("ax","") and ("a","x") concatenate to the same
// string without a delimiter, which would let an attacker swap their
// ciphertexts. SQLite enforces that neither name nor scope contains NUL in
// practice (the column is TEXT and the app never writes NUL-containing
// identifiers), so the delimiter is unambiguous.
func rowAAD(name, scope string) []byte {
	aad := make([]byte, 0, len(name)+1+len(scope))
	aad = append(aad, name...)
	aad = append(aad, 0x00)
	aad = append(aad, scope...)
	return aad
}

// encryptRow encrypts a secret-row plaintext under dek using AES-256-GCM with
// a fresh random nonce and AAD = name || 0x00 || scope. Returns
// (ciphertext, nonce, error). dek must be 32 bytes.
//
// Why AAD: binding ciphertext to row identity defends against a DB-write-
// capable attacker who could otherwise copy the ciphertext from one row
// (e.g. a throwaway test secret) into another (e.g. the production GitHub
// token) to inject the wrong credential on request. GCM authentication
// rejects any decrypt whose AAD doesn't match the one used at encrypt, so
// the store surfaces a decrypt error instead of silently returning attacker-
// chosen plaintext.
func encryptRow(dek []byte, name, scope string, plaintext []byte) (ciphertext, nonce []byte, err error) {
	if len(dek) != 32 {
		return nil, nil, errors.New("encryptRow: dek must be 32 bytes")
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, nil, fmt.Errorf("encryptRow: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("encryptRow: create GCM: %w", err)
	}
	nonce = make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("encryptRow: generate nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, rowAAD(name, scope))
	return ciphertext, nonce, nil
}

// decryptRow decrypts a ciphertext previously produced by encryptRow, using
// the same (name, scope) as AAD. On any authentication failure — wrong dek,
// wrong name, wrong scope, tampered ciphertext, tampered nonce — returns a
// non-nil error and a nil plaintext; GCM never yields partial plaintext on
// tag mismatch.
func decryptRow(dek []byte, name, scope string, ciphertext, nonce []byte) ([]byte, error) {
	if len(dek) != 32 {
		return nil, errors.New("decryptRow: dek must be 32 bytes")
	}
	if len(nonce) != nonceSize {
		return nil, errors.New("decryptRow: invalid nonce length")
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("decryptRow: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("decryptRow: create GCM: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, rowAAD(name, scope))
	if err != nil {
		return nil, fmt.Errorf("decryptRow: decrypt: %w", err)
	}
	return plain, nil
}
