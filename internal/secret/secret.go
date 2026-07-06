// Package secret provides authenticated encryption (AES-256-GCM) for small secrets stored at rest —
// specifically the secret access keys of UI-managed S3 connections kept in Postgres. The master key comes
// from ASTRO_ENCRYPTION_KEY (base64 std, 32 bytes) or, when unset, is generated once and persisted to a
// key file kept OUTSIDE the backup-able data/library/output roots, so a database dump alone never leaks the
// keys. Losing/rotating the key makes existing ciphertexts undecryptable (the connection must be re-entered).
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const keyLen = 32 // AES-256

// Box seals and opens small secrets with a process-wide key.
type Box struct{ aead cipher.AEAD }

// NewBox resolves the master key (ASTRO_ENCRYPTION_KEY first, else the key file — auto-generated once) and
// builds the AEAD. encryptionKey is base64 std of 32 bytes; keyFile "" → a default under the user config dir.
func NewBox(encryptionKey, keyFile string) (*Box, error) {
	key, err := resolveKey(encryptionKey, keyFile)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal returns nonce||ciphertext for plaintext (safe to store as bytea).
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal; it fails if the blob was truncated or the key/ciphertext don't match.
func (b *Box) Open(blob []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("secret: ciphertext too short")
	}
	out, err := b.aead.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("secret: decrypt failed (wrong ASTRO_ENCRYPTION_KEY?): %w", err)
	}
	return out, nil
}

func resolveKey(encryptionKey, keyFile string) ([]byte, error) {
	if encryptionKey != "" {
		key, err := base64.StdEncoding.DecodeString(encryptionKey)
		if err != nil {
			return nil, fmt.Errorf("secret: ASTRO_ENCRYPTION_KEY must be base64 std: %w", err)
		}
		if len(key) != keyLen {
			return nil, fmt.Errorf("secret: ASTRO_ENCRYPTION_KEY must decode to %d bytes, got %d", keyLen, len(key))
		}
		return key, nil
	}
	return keyFromFile(keyFile)
}

// keyFromFile reads (or first-time generates) a persisted random key. Exclusive create makes concurrent
// first-runs race-safe: a loser reads the winner's key rather than overwriting it.
func keyFromFile(path string) ([]byte, error) {
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("secret: no key file path and no user config dir: %w", err)
		}
		path = filepath.Join(dir, "astrostack", "secret.key")
	}
	if key, err := readKeyFile(path); err == nil {
		return key, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return readKeyFile(path) // someone raced us to create it — use theirs
		}
		return nil, fmt.Errorf("secret: create key file %s: %w", path, err)
	}
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key)); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func readKeyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != keyLen {
		return nil, fmt.Errorf("secret: key file %s is corrupt", path)
	}
	return key, nil
}
