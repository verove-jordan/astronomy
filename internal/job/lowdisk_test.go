package job

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/transfer"
)

func TestLowDiskActive(t *testing.T) {
	base := RunRequest{Mode: "deepsky", StorageMode: "s3", S3: &S3Target{Bucket: "b"}}
	on := &Manager{cfg: &config.Config{S3LowDisk: true}}
	off := &Manager{cfg: &config.Config{S3LowDisk: false}}
	with := func(mut func(*RunRequest)) RunRequest { r := base; mut(&r); return r }
	yes, no := true, false

	assert.True(t, on.lowDiskActive(base), "deepsky S3 run, server default on")
	assert.True(t, on.lowDiskActive(with(func(r *RunRequest) { r.Mode = "nebula" })), "nebula too")
	assert.False(t, off.lowDiskActive(base), "server default off")

	// Per-run override wins either way.
	assert.False(t, on.lowDiskActive(with(func(r *RunRequest) { r.LowDisk = &no })))
	assert.True(t, off.lowDiskActive(with(func(r *RunRequest) { r.LowDisk = &yes })))

	// Excluded cases.
	assert.False(t, on.lowDiskActive(with(func(r *RunRequest) { r.Mode = "milkyway" })), "not deep-sky/nebula")
	assert.False(t, on.lowDiskActive(with(func(r *RunRequest) { r.StorageMode = "local" })), "not full-S3")
	assert.False(t, on.lowDiskActive(with(func(r *RunRequest) { r.Rerun = &RerunRequest{} })), "rerun re-finishes from disk")
	assert.False(t, on.lowDiskActive(with(func(r *RunRequest) { r.DenoiseFinal = &DenoiseFinalRequest{} })), "denoise re-finishes from disk")
}

func TestStagerHelpers(t *testing.T) {
	s := &s3Stager{dataDir: "/data", roots: []string{"M92"}}
	// splitByRoot groups absolute paths under a root into folder-relative rels; paths outside are dropped.
	got := s.splitByRoot([]string{"/data/M92/L/a.fits", "/data/M92/R/b.fits", "/elsewhere/x.fits"})
	assert.Equal(t, map[string]map[string]bool{"M92": {"L/a.fits": true, "R/b.fits": true}}, got)

	assert.EqualValues(t, 30, planBytes([]transfer.PlannedFile{{Size: 10}, {Size: 20}}))
	assert.True(t, isFrameKey("lum/M92/2026/L/a.fits"))
	assert.False(t, isFrameKey("lum/M92/2026/info.txt"))

	fr := frameFromRow(store.FrameRow{Path: "/data/M92/L/a.fits", FrameType: "LIGHT", Filter: "L", Gain: 200, Bin: 0, ExposureMs: 120000})
	assert.Equal(t, "L", fr.Filter)
	assert.EqualValues(t, 200, fr.Gain)
	assert.Equal(t, 1, fr.BinX, "bin 0 normalized to 1")
	assert.Equal(t, "LIGHT", string(fr.Type))
}
