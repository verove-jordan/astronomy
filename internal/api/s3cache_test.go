package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

func TestS3Cache_Client_Reuse(t *testing.T) {
	c := newS3Cache()
	cfg := s3store.Config{Endpoint: "localhost:9000", AccessKeyID: "ak", SecretKey: "sk"}

	cl1, err := c.client(cfg)
	require.NoError(t, err)
	require.NotNil(t, cl1)

	cl2, err := c.client(cfg)
	require.NoError(t, err)
	assert.Same(t, cl1, cl2, "same config must reuse the same client (keep-alive)")

	rotated := cfg
	rotated.SecretKey = "sk2"
	cl3, err := c.client(rotated)
	require.NoError(t, err)
	assert.NotSame(t, cl1, cl3, "a rotated secret must yield a fresh client")
}

func TestFingerprint_DistinctPerField(t *testing.T) {
	base := s3store.Config{Endpoint: "e", Region: "r", AccessKeyID: "a", SecretKey: "s", UseSSL: false}
	tests := []struct {
		name   string
		mutate func(*s3store.Config)
	}{
		{"endpoint", func(c *s3store.Config) { c.Endpoint = "e2" }},
		{"region", func(c *s3store.Config) { c.Region = "r2" }},
		{"access key", func(c *s3store.Config) { c.AccessKeyID = "a2" }},
		{"secret", func(c *s3store.Config) { c.SecretKey = "s2" }},
		{"ssl", func(c *s3store.Config) { c.UseSSL = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := base
			tt.mutate(&other)
			assert.NotEqual(t, fingerprint(base), fingerprint(other))
		})
	}
	assert.Equal(t, fingerprint(base), fingerprint(base), "same config → stable fingerprint")
}
