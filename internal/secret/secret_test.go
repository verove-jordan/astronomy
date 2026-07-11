package secret

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBox_RoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32)) // all-zero 32-byte key is fine for a test
	b, err := NewBox(key, "")
	require.NoError(t, err)

	secret := []byte("super-secret-access-key")
	sealed, err := b.Seal(secret)
	require.NoError(t, err)
	assert.NotContains(t, string(sealed), "super-secret", "plaintext must not appear in the ciphertext")

	got, err := b.Open(sealed)
	require.NoError(t, err)
	assert.Equal(t, secret, got)
}

func TestBox_SealIsNondeterministic(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	b, _ := NewBox(key, "")
	a, _ := b.Seal([]byte("x"))
	c, _ := b.Seal([]byte("x"))
	assert.NotEqual(t, a, c, "a fresh nonce per Seal makes the ciphertext differ each time")
}

func TestBox_OpenRejectsWrongKeyAndTruncation(t *testing.T) {
	k1 := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	k2 := base64.StdEncoding.EncodeToString([]byte("ZZZZZZZZZZabcdef0123456789abcdef"))
	b1, _ := NewBox(k1, "")
	b2, _ := NewBox(k2, "")

	sealed, _ := b1.Seal([]byte("hello"))
	_, err := b2.Open(sealed)
	assert.Error(t, err, "the wrong key must not decrypt")

	_, err = b1.Open(sealed[:3])
	assert.Error(t, err, "a truncated blob must be rejected")
}

func TestNewBox_BadKey(t *testing.T) {
	_, err := NewBox("not-base64!!", "")
	assert.Error(t, err)
	_, err = NewBox(base64.StdEncoding.EncodeToString(make([]byte, 16)), "") // too short for AES-256
	assert.Error(t, err)
}

// With no env key, the box generates and persists a key file, and a second box reads the same key back
// (so ciphertext survives a process restart).
func TestKeyFile_GeneratedAndStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secret.key")

	b1, err := NewBox("", path)
	require.NoError(t, err)
	sealed, err := b1.Seal([]byte("persist-me"))
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "key file must be owner-only")

	b2, err := NewBox("", path) // simulates a restart reading the same file
	require.NoError(t, err)
	got, err := b2.Open(sealed)
	require.NoError(t, err)
	assert.Equal(t, "persist-me", string(got))
}
