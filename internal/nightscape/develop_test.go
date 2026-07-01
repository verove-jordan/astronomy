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
	assert.Equal(t, "cw-flip", resolveOrientation(Options{Orientation: "CW-Flip"}))
	assert.Equal(t, "180", resolveOrientation(Options{Orientation: "180"}))
	// auto/empty with no source frame → the content-heuristic fallback token.
	assert.Equal(t, "auto", resolveOrientation(Options{Orientation: "auto"}))
	assert.Equal(t, "auto", resolveOrientation(Options{}))
}
