package nightscape

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTiffOrientation_DNG(t *testing.T) {
	const dng = "../../input/MilkyWay/13_05_2026/Sorted_DNG/IMG_9565.DNG"
	if _, err := os.Stat(dng); err != nil {
		t.Skip("sample DNG not present")
	}
	assert.Equal(t, 6, tiffOrientation(dng), "portrait iPhone DNG stores EXIF orientation 6")
	code, w, h := tiffOrientationDims(dng)
	assert.Equal(t, 6, code)
	assert.Greater(t, w, 0, "IFD0 width must be readable")
	assert.Greater(t, h, 0, "IFD0 height must be readable")
}

func TestExifTokenFromCode(t *testing.T) {
	tests := []struct {
		code  int
		token string
		ok    bool
	}{
		{1, "none", true},
		{2, "flip", true},
		{3, "180", true},
		{4, "180-flip", true},
		{5, "cw-flip", true},
		{6, "cw", true}, // portrait shot on the common phone orientation
		{7, "ccw-flip", true},
		{8, "ccw", true},
		{0, "", false}, // unreadable / absent
		{9, "", false}, // out of range
	}
	for _, tt := range tests {
		tok, ok := exifTokenFromCode(tt.code)
		assert.Equal(t, tt.ok, ok, "code %d ok", tt.code)
		assert.Equal(t, tt.token, tok, "code %d token", tt.code)
	}
}

func TestResolveOrientation_Precedence(t *testing.T) {
	// An explicit transform is returned verbatim (lower-cased), never overridden by EXIF/auto.
	assert.Equal(t, "cw-flip", resolveOrientation(Options{Orientation: "CW-Flip"}, 4032, 3024))
	assert.Equal(t, "180", resolveOrientation(Options{Orientation: "180"}, 4032, 3024))
	// "auto" is now an explicit opt-in to the content heuristic.
	assert.Equal(t, "auto", resolveOrientation(Options{Orientation: "auto"}, 4032, 3024))
	// Default ("exif"/"") with no source frame → none: never guess a rotation.
	assert.Equal(t, "none", resolveOrientation(Options{}, 4032, 3024))
	assert.Equal(t, "none", resolveOrientation(Options{Orientation: "exif"}, 4032, 3024))
}

func TestOrientDecision(t *testing.T) {
	const (
		srcLandscapeW, srcLandscapeH = 4032, 3024
	)
	tests := []struct {
		name string
		dev  string
		dng  bool
		code int
		srcW int
		srcH int
		devW int
		devH int
		want string
	}{
		// dcraw develops with -t 0: never baked, always apply the EXIF token.
		{"dcraw portrait phone", "dcraw_emu", true, 6, srcLandscapeW, srcLandscapeH, 4032, 3024, "cw"},
		{"dcraw upside down", "dcraw_emu", true, 3, srcLandscapeW, srcLandscapeH, 4032, 3024, "180"},
		{"dcraw upright", "dcraw_emu", true, 1, srcLandscapeW, srcLandscapeH, 4032, 3024, "none"},

		// sips on DNG: verified it drops the tag and keeps sensor pixels → apply the token.
		{"sips DNG portrait", "sips", true, 6, srcLandscapeW, srcLandscapeH, 4032, 3024, "cw"},

		// sips on a non-raw with a rotated code: dims transposed = baked → nothing to apply.
		{"sips heic already baked", "sips", false, 6, srcLandscapeW, srcLandscapeH, 3024, 4032, "none"},
		// same code but dims NOT transposed = not baked → apply.
		{"sips heic unbaked", "sips", false, 6, srcLandscapeW, srcLandscapeH, 4032, 3024, "cw"},
		// unknown source dims: sensor assumed landscape; a portrait develop means baked.
		{"sips unknown src dims baked", "sips", false, 6, 0, 0, 3024, 4032, "none"},
		{"sips unknown src dims unbaked", "sips", false, 6, 0, 0, 4032, 3024, "cw"},
		// non-transposing codes are undetectable → assume ImageIO baked them.
		{"sips 180 non-raw assumed baked", "sips", false, 3, srcLandscapeW, srcLandscapeH, 4032, 3024, "none"},

		// native stills (no developer): pixels as stored → apply.
		{"native jpeg portrait", "", false, 6, 0, 0, 4032, 3024, "cw"},

		// unreadable tag → never guess.
		{"no tag", "dcraw_emu", true, 0, srcLandscapeW, srcLandscapeH, 4032, 3024, "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := orientDecision(tt.dev, tt.dng, tt.code, tt.srcW, tt.srcH, tt.devW, tt.devH)
			assert.Equal(t, tt.want, got)
		})
	}
}
