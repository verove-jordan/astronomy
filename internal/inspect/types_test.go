package inspect

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A camera raw is a Bayer mosaic on disk, but finalizeRawTypes stamps Channels = 3 on it (to mark it
// one-shot-color for the paths that develop it to RGB) and no raw carries a BAYERPAT card — so both
// header terms read "already RGB". Judging those terms alone is what stacked a Nikon NEF deep-sky
// session in MONOCHROME, so the extension has to decide for raws.
func TestFrame_NeedsDebayer(t *testing.T) {
	tests := []struct {
		name  string
		frame Frame
		want  bool
	}{
		// Camera raws: mosaic on disk whatever the metadata claims.
		{"nikon nef", Frame{Path: "/x/DSC_0001.NEF", Channels: 3}, true},
		{"canon cr2", Frame{Path: "/x/IMG_0001.cr2", Channels: 3}, true},
		{"canon cr3", Frame{Path: "/x/IMG_0001.cr3", Channels: 3}, true},
		{"sony arw", Frame{Path: "/x/DSC00001.arw", Channels: 3}, true},
		{"fuji raf", Frame{Path: "/x/DSCF0001.raf", Channels: 3}, true},
		{"adobe dng", Frame{Path: "/x/IMG_0001.dng", Channels: 3}, true},
		{"extension match is case-insensitive", Frame{Path: "/x/a.NeF", Channels: 3}, true},

		// Already-demosaiced colour: debayering these corrupts them.
		{"heic is developed, not a mosaic", Frame{Path: "/x/IMG_0001.heic", Channels: 3}, false},
		{"heif is developed, not a mosaic", Frame{Path: "/x/IMG_0001.heif", Channels: 3}, false},
		{"colour tiff", Frame{Path: "/x/a.tif", Channels: 3}, false},
		{"jpeg", Frame{Path: "/x/a.jpg", Channels: 3}, false},

		// FITS keeps deciding on its header, exactly as before.
		{"fits with bayer card", Frame{Path: "/x/a.fits", Bayer: "RGGB", Channels: 1}, true},
		{"fits already debayered", Frame{Path: "/x/a.fits", Bayer: "RGGB", Channels: 3}, false},
		{"monochrome fits", Frame{Path: "/x/a.fits", Channels: 1}, false},
		{"rgb fits", Frame{Path: "/x/a.fits", Channels: 3}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.frame.NeedsDebayer())
		})
	}
}

// A CFA raw is colour too — NeedsDebayer and IsColor must not disagree about it, or a frame would be
// demosaiced by calibration while being grouped as monochrome.
func TestFrame_IsColor_CameraRaw(t *testing.T) {
	fr := Frame{Path: "/x/DSC_0001.NEF", Channels: 3}
	assert.True(t, fr.IsColor())
	assert.True(t, fr.NeedsDebayer())
}

// cameraRawExts is now assembled from the mosaic and developed sets rather than written out, so pin
// the union: dropping an extension here would stop a whole camera's files being seen as lights.
func TestCameraRawExts_Union(t *testing.T) {
	want := []string{".dng", ".heic", ".heif", ".cr2", ".cr3", ".nef", ".arw", ".raf"}
	assert.Len(t, cameraRawExts, len(want))
	for _, ext := range want {
		assert.True(t, cameraRawExts[ext], "cameraRawExts must contain %s", ext)
	}
	// The split is the point: every member is in exactly one of the two halves.
	for ext := range cameraRawExts {
		assert.NotEqual(t, cfaRawExts[ext], developedStillExts[ext],
			"%s must be either a mosaic raw or a developed still, not both or neither", ext)
	}
}
