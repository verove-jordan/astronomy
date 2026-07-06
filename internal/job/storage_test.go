package job

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/config"
)

// dataInputRels maps a run's capture folders to DataDir-relative keys for the full-S3 pull/free steps — it
// must dedup, preserve order, and drop anything that escapes the data dir (so a stray path can never widen
// the transfer beyond the mirror).
func TestDataInputRels(t *testing.T) {
	m := &Manager{cfg: &config.Config{DataDir: "/data"}}
	cases := []struct {
		name string
		req  RunRequest
		want []string
	}{
		{"single path", RunRequest{Path: "/data/M101/L"}, []string{"M101/L"}},
		{"paths dedup + order preserved", RunRequest{Paths: []string{"/data/A", "/data/A", "/data/B"}}, []string{"A", "B"}},
		{"skips paths outside the data dir", RunRequest{Paths: []string{"/data/A", "/etc/passwd", "/other/B"}}, []string{"A"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, m.dataInputRels(tc.req))
		})
	}
}

// wantsS3Storage gates the full-S3 orchestration: only a run explicitly in "s3" mode with a bucket, and not
// a transfer / live / refine job (those manage their own I/O), pulls-and-frees.
func TestWantsS3Storage(t *testing.T) {
	base := RunRequest{StorageMode: "s3", S3: &S3Target{Bucket: "b"}}
	assert.True(t, base.wantsS3Storage())

	assert.False(t, RunRequest{StorageMode: "local", S3: &S3Target{Bucket: "b"}}.wantsS3Storage(), "local mode")
	assert.False(t, RunRequest{StorageMode: "s3"}.wantsS3Storage(), "no S3 target")
	assert.False(t, RunRequest{StorageMode: "s3", S3: &S3Target{}}.wantsS3Storage(), "empty bucket")

	live := base
	live.Live = &LiveRequest{}
	assert.False(t, live.wantsS3Storage(), "live-stacking manages its own I/O")

	xfer := base
	xfer.Transfer = &TransferRequest{}
	assert.False(t, xfer.wantsS3Storage(), "transfer jobs are not pipeline runs")

	refine := base
	refine.Refine = &RefineRequest{}
	assert.False(t, refine.wantsS3Storage(), "refine re-finishes an existing run")
}
