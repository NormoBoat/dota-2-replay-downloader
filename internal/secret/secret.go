// Package secret encrypts account passwords, tokens and proxy passwords (AES-256-GCM).
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

type Box struct{ aead cipher.AEAD }

func New(key []byte) (*Box, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead}, nil
}

// Seal returns nonce||ciphertext.
func (b *Box) Seal(plain string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, []byte(plain), nil)
}

// SealOpt is Seal for optional values: "" stays NULL.
func (b *Box) SealOpt(plain string) []byte {
	if plain == "" {
		return nil
	}
	return b.Seal(plain)
}

func (b *Box) Open(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("secret: ciphertext too short")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", errors.New("secret: cannot decrypt (wrong security.secret_key?)")
	}
	return string(plain), nil
}

// NewKey returns a random 32-byte key in base64 for settings.toml.
func NewKey() string {
	k := make([]byte, 32)
	rand.Read(k)
	return base64.StdEncoding.EncodeToString(k)
}
