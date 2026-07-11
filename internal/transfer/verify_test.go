package transfer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// md5bbb is md5("bbb") — the content of lights/b.fits in writeTestFolder (md5aaaaa/md5XXXXX live in
// removelocal_test.go, same package).
const md5bbb = "08f8e0260c64418510cefb2b06eee5cd"

// The keys writeTestFolder's two files mirror to under KeyPrefix "acct/data" + RelPath "M101".
const (
	keyA = "acct/data/M101/a.fits"        // "aaaaa" → md5aaaaa, size 5
	keyB = "acct/data/M101/lights/b.fits" // "bbb"   → md5bbb,   size 3
)

// A verified sync (Request.Verify) uploads exactly the files that are missing, wrong-size, or
// same-size-but-corrupted on S3, and skips only those whose bytes match — the "upload only what's missing
// or corrupted" contract.
func TestRunUpload_SyncVerify(t *testing.T) {
	future := time.Now().Add(time.Hour).UnixMilli() // object uploaded after the local file was written

	tests := []struct {
		name        string
		seed        map[string]s3store.Object
		wantUpload  []string // keys expected to be (re-)uploaded
		wantSkipped int
	}{
		{
			name: "all present and correct: skip everything",
			seed: map[string]s3store.Object{
				keyA: {Key: keyA, Size: 5, MD5: md5aaaaa, ModTime: future},
				keyB: {Key: keyB, Size: 3, MD5: md5bbb, ModTime: future},
			},
			wantUpload:  nil,
			wantSkipped: 2,
		},
		{
			name: "missing object is uploaded",
			seed: map[string]s3store.Object{
				keyB: {Key: keyB, Size: 3, MD5: md5bbb, ModTime: future},
			},
			wantUpload:  []string{keyA},
			wantSkipped: 1,
		},
		{
			name: "same size but corrupted (wrong md5) is re-uploaded",
			seed: map[string]s3store.Object{
				keyA: {Key: keyA, Size: 5, MD5: md5XXXXX, ModTime: future}, // same size, different bytes
				keyB: {Key: keyB, Size: 3, MD5: md5bbb, ModTime: future},
			},
			wantUpload:  []string{keyA},
			wantSkipped: 1,
		},
		{
			name: "wrong size is re-uploaded",
			seed: map[string]s3store.Object{
				keyA: {Key: keyA, Size: 4, MD5: md5aaaaa, ModTime: future}, // truncated mirror
				keyB: {Key: keyB, Size: 3, MD5: md5bbb, ModTime: future},
			},
			wantUpload:  []string{keyA},
			wantSkipped: 1,
		},
		{
			name:        "empty mirror: everything is uploaded",
			seed:        map[string]s3store.Object{},
			wantUpload:  []string{keyA, keyB},
			wantSkipped: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := writeTestFolder(t)
			req.Op = OpSync
			req.Verify = true
			fake := newFakeS3()
			for k, o := range tt.seed {
				fake.objects[k] = o
			}

			res, err := runUpload(context.Background(), fake, req, true, nil)

			require.NoError(t, err)
			assert.Equal(t, len(tt.wantUpload), res.Files, "uploaded count")
			assert.Equal(t, tt.wantSkipped, res.Skipped, "skipped count")
			for _, k := range tt.wantUpload {
				assert.Contains(t, fake.uploadCalls, k, "expected (re-)upload of %s", k)
			}
		})
	}
}

// Verify is exactly what tells a corrupted same-size mirror apart from a good one: the default size-only
// sync trusts it (fast, immutable-capture assumption) and skips; the verified sync re-uploads it.
func TestRunUpload_Sync_VerifyVsSizeOnly_CorruptedSameSize(t *testing.T) {
	seed := func(fake *fakeS3) {
		// a.fits is on S3 at the right SIZE but the wrong BYTES (a bad/interrupted earlier upload).
		fake.objects[keyA] = s3store.Object{Key: keyA, Size: 5, MD5: md5XXXXX}
		fake.objects[keyB] = s3store.Object{Key: keyB, Size: 3, MD5: md5bbb}
	}

	t.Run("size-only sync skips the corrupted same-size object", func(t *testing.T) {
		req, _ := writeTestFolder(t)
		req.Op = OpSync // Verify defaults false
		fake := newFakeS3()
		seed(fake)

		res, err := runUpload(context.Background(), fake, req, true, nil)

		require.NoError(t, err)
		assert.Equal(t, 0, res.Files, "size-only sync uploads nothing")
		assert.Equal(t, 2, res.Skipped)
		assert.NotContains(t, fake.uploadCalls, keyA)
	})

	t.Run("verified sync re-uploads the corrupted same-size object", func(t *testing.T) {
		req, _ := writeTestFolder(t)
		req.Op = OpSync
		req.Verify = true
		fake := newFakeS3()
		seed(fake)

		res, err := runUpload(context.Background(), fake, req, true, nil)

		require.NoError(t, err)
		assert.Equal(t, 1, res.Files, "verified sync re-uploads the corrupted file")
		assert.Equal(t, 1, res.Skipped, "the good file is still skipped")
		assert.Contains(t, fake.uploadCalls, keyA)
		assert.NotContains(t, fake.uploadCalls, keyB)
	})
}

// A verified sync records the key of every file it skips (OnStored) just like the size-only sync, so the
// classified-key ledger self-heals even when nothing is transferred.
func TestRunUpload_SyncVerify_RecordsSkips(t *testing.T) {
	req, _ := writeTestFolder(t)
	req.Op = OpSync
	req.Verify = true
	stored := map[string]string{}
	req.OnStored = func(rel, key string, _ int64) { stored[rel] = key }
	fake := newFakeS3()
	fake.objects[keyA] = s3store.Object{Key: keyA, Size: 5, MD5: md5aaaaa}
	fake.objects[keyB] = s3store.Object{Key: keyB, Size: 3, MD5: md5bbb}

	res, err := runUpload(context.Background(), fake, req, true, nil)

	require.NoError(t, err)
	assert.Equal(t, 0, res.Files)
	assert.Equal(t, map[string]string{"a.fits": keyA, "lights/b.fits": keyB}, stored)
}
