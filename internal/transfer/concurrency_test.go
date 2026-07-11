package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRequest_Concurrency: the effective parallel-file count defaults when unset/≤0 and is clamped to the
// ceiling, so a bad override can never fan out unboundedly.
func TestRequest_Concurrency(t *testing.T) {
	assert.Equal(t, defaultConcurrency, Request{}.concurrency(), "unset → default")
	assert.Equal(t, defaultConcurrency, Request{Concurrency: -3}.concurrency(), "negative → default")
	assert.Equal(t, 3, Request{Concurrency: 3}.concurrency())
	assert.Equal(t, maxConcurrency, Request{Concurrency: 10000}.concurrency(), "clamped to the ceiling")
}

// TestRunUpload_ParallelUploadsEveryFile: fanning out across workers still uploads EVERY file exactly once,
// records them all, and the aggregated byte progress ends exactly at the folder total (no double-count, no
// overshoot) — the correctness contract the parallel loop must keep. Run under -race, it also guards the
// progress/ledger against data races.
func TestRunUpload_ParallelUploadsEveryFile(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "M42")
	require.NoError(t, os.MkdirAll(folder, 0o755))
	const n = 50
	var wantBytes int64
	for i := 0; i < n; i++ {
		body := []byte(fmt.Sprintf("frame-%03d-payload-bytes", i))
		require.NoError(t, os.WriteFile(filepath.Join(folder, fmt.Sprintf("f%03d.fits", i)), body, 0o644))
		wantBytes += int64(len(body))
	}
	req := Request{Op: OpUpload, LocalRoot: root, RelPath: "M42", Bucket: "b", KeyPrefix: "acct/data", Concurrency: 8}
	fake := newFakeS3()

	var lastBytes int64 // written only from the serialized progress callback, read after Run returns
	res, err := runUpload(context.Background(), fake, req, false, func(p Progress) {
		assert.Equal(t, wantBytes, p.BytesTotal)
		assert.LessOrEqual(t, p.BytesDone, p.BytesTotal, "aggregated progress never overshoots the total")
		lastBytes = p.BytesDone
	})
	require.NoError(t, err)
	assert.Equal(t, n, res.Files)
	assert.Equal(t, wantBytes, res.Bytes)
	assert.Len(t, fake.objects, n, "every file mirrored")
	assert.Len(t, fake.uploadCalls, n, "every file uploaded exactly once")
	assert.Equal(t, wantBytes, lastBytes, "progress ends at the full byte total")
}
