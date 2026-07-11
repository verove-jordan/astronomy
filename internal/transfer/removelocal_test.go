package transfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

const (
	md5aaaaa = "594f803b380a41396ed63dca39503542" // md5("aaaaa")
	md5XXXXX = "d21c9d881eba6988be480efab45de2b9" // md5("XXXXX") — same size, different bytes
)

func TestVerifyMirrored_Tiers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.fits")
	require.NoError(t, os.WriteFile(p, []byte("aaaaa"), 0o644))
	f := localFile{path: p, size: 5}
	future := time.Now().Add(time.Hour).UnixMilli() // object uploaded after the local file was written

	tests := []struct {
		name       string
		obj        s3store.Object
		exists     bool
		wantOK     bool
		wantLegacy bool
	}{
		{"missing object", s3store.Object{}, false, false, false},
		{"size mismatch", s3store.Object{Size: 4}, true, false, false},
		{"tier 2: metadata md5 match", s3store.Object{Size: 5, MD5: md5aaaaa, ETag: "whatever-2"}, true, true, false},
		{"tier 2: metadata md5 mismatch (same size!)", s3store.Object{Size: 5, MD5: md5XXXXX}, true, false, false},
		{"tier 3: single-part etag match", s3store.Object{Size: 5, ETag: md5aaaaa}, true, true, false},
		{"tier 3: single-part etag mismatch (same size!)", s3store.Object{Size: 5, ETag: md5XXXXX}, true, false, false},
		{"tier 3: uppercase md5 still matches", s3store.Object{Size: 5, ETag: "594F803B380A41396ED63DCA39503542"}, true, true, false},
		{"tier 4: legacy multipart accepted on size", s3store.Object{Size: 5, ETag: md5XXXXX + "-3", ModTime: future}, true, true, true},
		{"tier 4: legacy without mtime accepted on size", s3store.Object{Size: 5, ETag: md5XXXXX + "-3"}, true, true, true},
		{"tier 4: local modified after upload rejected", s3store.Object{Size: 5, ETag: md5XXXXX + "-3", ModTime: 1}, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeS3()
			if tt.exists {
				o := tt.obj
				o.Key = "k"
				fake.objects["k"] = o
			}
			ok, legacy, err := verifyMirrored(context.Background(), fake, "b", "k", f)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOK, ok, "ok")
			assert.Equal(t, tt.wantLegacy, legacy, "legacy")
		})
	}
}

// seedMirror registers both test files on the fake as strong (metadata-MD5) mirrors.
func seedMirror(fake *fakeS3, modTime int64) {
	fake.objects["acct/data/M101/a.fits"] = s3store.Object{
		Key: "acct/data/M101/a.fits", Size: 5, MD5: md5aaaaa, ModTime: modTime,
	}
	fake.objects["acct/data/M101/lights/b.fits"] = s3store.Object{
		Key: "acct/data/M101/lights/b.fits", Size: 3, MD5: "08f8e0260c64418510cefb2b06eee5cd", ModTime: modTime,
	}
}

func TestRunRemoveLocal_DeletesWhenStronglyVerified(t *testing.T) {
	req, folder := writeTestFolder(t)
	req.Op = OpRemoveLocal
	fake := newFakeS3()
	seedMirror(fake, time.Now().Add(time.Hour).UnixMilli())

	res, err := runRemoveLocal(context.Background(), fake, req)

	require.NoError(t, err)
	assert.Equal(t, 2, res.Files)
	assert.Empty(t, res.Warnings)
	assert.NoFileExists(t, filepath.Join(folder, "a.fits"))
	assert.NoFileExists(t, filepath.Join(folder, "lights", "b.fits"))
}

func TestRunRemoveLocal_AbortsAllOnOneMismatch(t *testing.T) {
	req, folder := writeTestFolder(t)
	req.Op = OpRemoveLocal
	fake := newFakeS3()
	seedMirror(fake, time.Now().Add(time.Hour).UnixMilli())
	// One object was overwritten with same-size different bytes (e.g. a bad manual re-upload).
	fake.objects["acct/data/M101/a.fits"] = s3store.Object{
		Key: "acct/data/M101/a.fits", Size: 5, MD5: md5XXXXX,
	}

	_, err := runRemoveLocal(context.Background(), fake, req)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing deleted")
	assert.FileExists(t, filepath.Join(folder, "a.fits"))
	assert.FileExists(t, filepath.Join(folder, "lights", "b.fits"), "abort-all: the verified file survives too")
}

func TestRunRemoveLocal_LegacyObjectsDeleteWithWarning(t *testing.T) {
	req, folder := writeTestFolder(t)
	req.Op = OpRemoveLocal
	fake := newFakeS3()
	future := time.Now().Add(time.Hour).UnixMilli()
	seedMirror(fake, future)
	// a.fits is a legacy multipart upload: no MD5 metadata, multipart ETag — only its size can vouch.
	fake.objects["acct/data/M101/a.fits"] = s3store.Object{
		Key: "acct/data/M101/a.fits", Size: 5, ETag: "0123abcd-7", ModTime: future,
	}

	res, err := runRemoveLocal(context.Background(), fake, req)

	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "verified by size only (legacy upload): a.fits", res.Warnings[0])
	assert.NoFileExists(t, filepath.Join(folder, "a.fits"))
}

func TestRunRemoveLocal_PlannedOnlyFreesOnlyPlanFiles(t *testing.T) {
	req, folder := writeTestFolder(t)
	req.Op = OpRemoveLocal
	req.PlannedOnly = true // low-disk staged free: only this wave's file
	req.Plan = []PlannedFile{{Rel: "a.fits", Key: "acct/data/M101/a.fits", Size: 5}}
	fake := newFakeS3()
	seedMirror(fake, time.Now().Add(time.Hour).UnixMilli())

	res, err := runRemoveLocal(context.Background(), fake, req)

	require.NoError(t, err)
	assert.Equal(t, 1, res.Files, "only the planned file is freed")
	assert.NoFileExists(t, filepath.Join(folder, "a.fits"))
	assert.FileExists(t, filepath.Join(folder, "lights", "b.fits"), "the unplanned file is left in place")
}

func TestRunRemoveLocal_PlannedOnlyNoPlanFreesNothing(t *testing.T) {
	req, folder := writeTestFolder(t)
	req.Op = OpRemoveLocal
	req.PlannedOnly = true // no Plan → safe no-op (never delete an unplanned file)
	fake := newFakeS3()
	seedMirror(fake, time.Now().Add(time.Hour).UnixMilli())

	res, err := runRemoveLocal(context.Background(), fake, req)

	require.NoError(t, err)
	assert.Equal(t, 0, res.Files)
	assert.FileExists(t, filepath.Join(folder, "a.fits"))
	assert.FileExists(t, filepath.Join(folder, "lights", "b.fits"))
}

func TestRunRemoveLocal_CtxCancelStopsDeleteLoop(t *testing.T) {
	req, folder := writeTestFolder(t)
	req.Op = OpRemoveLocal
	fake := newFakeS3()
	seedMirror(fake, time.Now().Add(time.Hour).UnixMilli())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // verification uses the fake (no ctx use); the delete loop must notice before removing anything

	_, err := runRemoveLocal(ctx, fake, req)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.FileExists(t, filepath.Join(folder, "a.fits"), "nothing deleted after cancellation")
	assert.FileExists(t, filepath.Join(folder, "lights", "b.fits"))
}
