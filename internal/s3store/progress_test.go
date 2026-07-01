package s3store

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountReader_ReportsBytes(t *testing.T) {
	const body = "the quick brown fox jumps over the lazy dog"
	var total int64
	r := &countReader{r: strings.NewReader(body), onBytes: func(d int64) { total += d }}

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
	assert.Equal(t, int64(len(body)), total, "reported bytes must equal the body length")
}

func TestCountReader_NilCallbackIsSafe(t *testing.T) {
	r := &countReader{r: strings.NewReader("abc")} // no onBytes
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "abc", string(got))
}

func TestConfig_Configured(t *testing.T) {
	assert.False(t, Config{}.Configured())
	assert.False(t, Config{AccessKeyID: "a"}.Configured())
	assert.True(t, Config{AccessKeyID: "a", SecretKey: "s"}.Configured())
}

func TestNew_NoCredentials(t *testing.T) {
	_, err := New(Config{})
	assert.ErrorIs(t, err, ErrNoCredentials)
}
