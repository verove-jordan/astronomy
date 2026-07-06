package s3store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// normalizeEndpoint must reduce whatever a user pastes to the bare host[:port] minio-go accepts — a scheme
// or trailing path otherwise trips "Endpoint url cannot have fully qualified paths".
func TestNormalizeEndpoint(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		"s3.amazonaws.com":                   "s3.amazonaws.com",
		"https://s3.fr-par.scw.cloud":        "s3.fr-par.scw.cloud",
		"http://localhost:9000":              "localhost:9000",
		"https://s3.fr-par.scw.cloud/":       "s3.fr-par.scw.cloud",
		"https://s3.fr-par.scw.cloud/bucket": "s3.fr-par.scw.cloud",
		"  https://minio.example:9000  ":     "minio.example:9000",
		"minio.example:9000?x=1":             "minio.example:9000",
		// Virtual-hosted-style (bucket in the host) → strip the bucket label down to the s3. service host.
		"astrophoto.s3.fr-par.scw.cloud":         "s3.fr-par.scw.cloud",
		"https://astrophoto.s3.fr-par.scw.cloud": "s3.fr-par.scw.cloud",
		"mybucket.s3.amazonaws.com":              "s3.amazonaws.com",
		"mybucket.s3.us-east-1.amazonaws.com":    "s3.us-east-1.amazonaws.com",
		// Region/base endpoints are left untouched (they start with "s3." — no bucket label to strip).
		"s3.us-east-1.amazonaws.com": "s3.us-east-1.amazonaws.com",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeEndpoint(in), in)
	}
}
