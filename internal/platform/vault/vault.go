// Package vault encrypts every secret Proxier stores and derives the keyed
// hashes used to look secrets up (ADR 0012). One master key from the
// environment is the root of both: losing it loses every secret.
//
// Two subkeys are derived from the master key with HKDF-SHA256, one for
// AES-256-GCM and one for HMAC-SHA256 lookups, so neither use weakens the
// other. A sealed blob is
//
//	version (1 byte) || nonce (12 bytes) || ciphertext+tag
//
// and its associated data binds it to where it is stored ("setting:<key>",
// "job:12:payload"): a blob copied into another row does not open.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the length of the master key.
const KeySize = 32

// TokenBytes is the entropy of a token from NewToken (256 bits).
const TokenBytes = 32

const version1 byte = 1

// ErrOpen is returned for a blob that is malformed, tampered with, sealed
// under another key, or sealed for another place.
var ErrOpen = errors.New("vault: cannot open sealed value")

// Vault seals and opens secrets under the master key.
type Vault struct {
	aead      cipher.AEAD
	lookupKey []byte
}

// New derives the vault's keys from a 32-byte master key.
func New(masterKey []byte) (*Vault, error) {
	if len(masterKey) != KeySize {
		return nil, fmt.Errorf("vault: master key must be %d bytes, got %d", KeySize, len(masterKey))
	}
	encKey, err := hkdf.Key(sha256.New, masterKey, nil, "proxier/vault/encrypt/v1", 32)
	if err != nil {
		return nil, fmt.Errorf("vault: derive encryption key: %w", err)
	}
	lookupKey, err := hkdf.Key(sha256.New, masterKey, nil, "proxier/vault/lookup/v1", 32)
	if err != nil {
		return nil, fmt.Errorf("vault: derive lookup key: %w", err)
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return &Vault{aead: aead, lookupKey: lookupKey}, nil
}

// Seal encrypts plaintext for the place named by aad, under a fresh nonce.
func (v *Vault) Seal(plaintext []byte, aad string) []byte {
	ns := v.aead.NonceSize()
	out := make([]byte, 1+ns, 1+ns+len(plaintext)+v.aead.Overhead())
	out[0] = version1
	rand.Read(out[1:]) // never fails (crypto/rand since Go 1.24)
	return v.aead.Seal(out, out[1:1+ns], plaintext, additional(out[0], aad))
}

// Open decrypts a blob produced by Seal with the same aad.
func (v *Vault) Open(blob []byte, aad string) ([]byte, error) {
	ns := v.aead.NonceSize()
	if len(blob) < 1+ns+v.aead.Overhead() || blob[0] != version1 {
		return nil, ErrOpen
	}
	pt, err := v.aead.Open(nil, blob[1:1+ns], blob[1+ns:], additional(blob[0], aad))
	if err != nil {
		return nil, ErrOpen
	}
	return pt, nil
}

// SealString is Seal for text.
func (v *Vault) SealString(plaintext, aad string) []byte { return v.Seal([]byte(plaintext), aad) }

// OpenString is Open for text.
func (v *Vault) OpenString(blob []byte, aad string) (string, error) {
	pt, err := v.Open(blob, aad)
	return string(pt), err
}

// Lookup returns the keyed hash under which a secret (a link token, a fetch
// URL token) is stored and found. The secret itself is never compared.
func (v *Vault) Lookup(secret string) []byte {
	m := hmac.New(sha256.New, v.lookupKey)
	m.Write([]byte(secret))
	return m.Sum(nil)
}

// NewToken returns a random URL-safe token with TokenBytes of entropy.
func NewToken() string {
	b := make([]byte, TokenBytes)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func additional(version byte, aad string) []byte {
	return append([]byte{version}, aad...)
}
