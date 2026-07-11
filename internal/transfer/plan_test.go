package transfer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// An upload with a classified Plan places each file at its planned key and records the mapping via OnStored.
func TestRunUpload_PlanPlacesClassifiedKeys(t *testing.T) {
	req, _ := writeTestFolder(t)
	req.Op = OpUpload
	req.Plan = []PlannedFile{
		{Rel: "a.fits", Key: "darks/set/a.fits", Size: 5},
		{Rel: "lights/b.fits", Key: "lum/M101/2020-01-01/b.fits", Size: 3},
	}
	stored := map[string]string{}
	req.OnStored = func(rel, key string, _ int64) { stored[rel] = key }
	fake := newFakeS3()

	res, err := runUpload(context.Background(), fake, req, false, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Files)
	assert.Contains(t, fake.uploadCalls, "darks/set/a.fits")
	assert.Contains(t, fake.uploadCalls, "lum/M101/2020-01-01/b.fits")
	assert.Equal(t, map[string]string{
		"a.fits":        "darks/set/a.fits",
		"lights/b.fits": "lum/M101/2020-01-01/b.fits",
	}, stored)
}

// A sync with a Plan skips a file already present at its classified key but STILL records it (self-heal),
// and skips one already at its LEGACY key without re-uploading.
func TestRunUpload_PlanSyncSkipsPresentAndRecords(t *testing.T) {
	req, _ := writeTestFolder(t)
	req.Op = OpSync
	req.Plan = []PlannedFile{
		{Rel: "a.fits", Key: "darks/set/a.fits", Size: 5},
		{Rel: "lights/b.fits", Key: "lum/M101/b.fits", Size: 3},
	}
	stored := map[string]string{}
	req.OnStored = func(rel, key string, _ int64) { stored[rel] = key }
	fake := newFakeS3()
	fake.objects["darks/set/a.fits"] = s3store.Object{Key: "darks/set/a.fits", Size: 5}                         // already classified
	fake.objects["acct/data/M101/lights/b.fits"] = s3store.Object{Key: "acct/data/M101/lights/b.fits", Size: 3} // legacy leftover

	res, err := runUpload(context.Background(), fake, req, true, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Skipped, "both files already mirrored (one classified, one legacy)")
	assert.Equal(t, 0, res.Files, "nothing re-uploaded")
	assert.Empty(t, fake.uploadCalls)
	assert.Equal(t, "darks/set/a.fits", stored["a.fits"])
	assert.Equal(t, "acct/data/M101/lights/b.fits", stored["lights/b.fits"], "legacy key recorded as-is")
}

// A download with a Plan reassembles the classified keys back into the local tree AND merges a
// legacy-keyed leftover not covered by the plan.
func TestRunDownload_PlanReassemblesTreeMergesLegacy(t *testing.T) {
	root := t.TempDir()
	req := Request{Op: OpDownload, LocalRoot: root, RelPath: "M101", Bucket: "b", KeyPrefix: "acct/data"}
	req.Plan = []PlannedFile{{Rel: "L/l1.fits", Key: "lum/M101/2020/L/l1.fits", Size: 4}}
	fake := newFakeS3()
	fake.objects["lum/M101/2020/L/l1.fits"] = s3store.Object{Key: "lum/M101/2020/L/l1.fits", Size: 4}
	fake.objects["acct/data/M101/legacy.fits"] = s3store.Object{Key: "acct/data/M101/legacy.fits", Size: 2}

	res, err := runDownload(context.Background(), fake, req, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Files, "classified + legacy leftover")
	assert.FileExists(t, filepath.Join(root, "M101", "L", "l1.fits"))
	assert.FileExists(t, filepath.Join(root, "M101", "legacy.fits"))
}
