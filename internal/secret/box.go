package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

const salt = "kookbot-v1"

// Purpose selects the HKDF info and the AES-GCM additional data.
// A ciphertext sealed for one purpose does not open as another.
type Purpose string

const (
	PurposeBotToken      Purpose = "bot_token"
	PurposeTTSAPIKey     Purpose = "tts_api_key"
	PurposeVoicePassword Purpose = "voice_password"
)

// ErrSecretMismatch means the key, nonce, or purpose does not open this ciphertext.
// The error text is only the code. It never wraps plaintext or ciphertext.
var ErrSecretMismatch = errors.New("secret_mismatch")

// Box seals small secrets with AES-256-GCM.
type Box struct {
	keys map[Purpose][]byte
}

func New(ikm []byte) (*Box, error) {
	if len(ikm) < 32 {
		return nil, errors.New("secret: ikm too short")
	}
	keys := make(map[Purpose][]byte, 3)
	for _, p := range []Purpose{PurposeBotToken, PurposeTTSAPIKey, PurposeVoicePassword} {
		key, err := derive(ikm, string(p))
		if err != nil {
			return nil, err
		}
		keys[p] = key
	}
	return &Box{keys: keys}, nil
}

func derive(ikm []byte, info string) ([]byte, error) {
	r := hkdf.New(sha256.New, ikm, []byte(salt), []byte(info))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

// Fingerprint is the first 8 hex characters of SHA-256(secret).
// It detects a rotated KOOKBOT_SECRET. It is not a key.
func Fingerprint(secret []byte) string {
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:])[:8]
}

// PasswordMatch compares SHA-256 digests so the check does not return early on length.
func PasswordMatch(got, want string) bool {
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

// Seal returns a fresh 12-byte nonce and the GCM ciphertext (including the tag).
func (b *Box) Seal(purpose Purpose, plaintext []byte) (nonce, ciphertext []byte, err error) {
	aead, err := b.aead(purpose)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = aead.Seal(nil, nonce, plaintext, []byte(purpose))
	return nonce, ciphertext, nil
}

// Open returns ErrSecretMismatch when the ciphertext cannot be authenticated.
func (b *Box) Open(purpose Purpose, nonce, ciphertext []byte) ([]byte, error) {
	aead, err := b.aead(purpose)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte(purpose))
	if err != nil {
		return nil, ErrSecretMismatch
	}
	return plain, nil
}

func (b *Box) aead(purpose Purpose) (cipher.AEAD, error) {
	key, ok := b.keys[purpose]
	if !ok {
		return nil, errors.New("secret: unknown purpose")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
