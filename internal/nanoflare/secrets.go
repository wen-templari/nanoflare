package nanoflare

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

type SecretCodec struct {
	key []byte
}

func NewSecretCodec(value string) (*SecretCodec, error) {
	key, err := parseSecretKey(value)
	if err != nil {
		return nil, err
	}
	return &SecretCodec{key: key}, nil
}

func parseSecretKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("NANOFLARE_SECRET_KEY is required")
	}
	if len(value) == 32 {
		return []byte(value), nil
	}
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	return nil, errors.New("NANOFLARE_SECRET_KEY must be 32 raw bytes, 64 hex characters, or base64 for 32 bytes")
}

func (c *SecretCodec) Encrypt(value string) ([]byte, []byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, []byte(value), nil), nil
}

func (c *SecretCodec) Decrypt(nonce, ciphertext []byte) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() {
		return "", fmt.Errorf("invalid secret nonce size %d", len(nonce))
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
