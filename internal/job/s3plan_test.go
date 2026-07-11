package job

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/s3layout"
)

// calibSetPrefix identifies a calibration set (darks/offsets/flats) so a collision date-suffixes the whole
// set; a light/legacy key is not a calib set.
func TestCalibSetPrefix(t *testing.T) {
	cases := map[string]string{
		"darks/darks_0gain_300s/Dark_0001.fits": "darks/darks_0gain_300s",
		"offsets/offset_250gain/b1.fits":        "offsets/offset_250gain",
		"flats/flats_Ha/f1.fits":                "flats/flats_Ha",
		"lum/M101/2020-03-13/L/l1.fits":         "",
		"data/misc/x.fits":                      "",
		"darks":                                 "",
	}
	for key, want := range cases {
		assert.Equal(t, want, calibSetPrefix(key), key)
	}
}

// suffixSet appends the session date to a colliding calib set's dir, keeping the file basename.
func TestSuffixSet(t *testing.T) {
	assert.Equal(t, "darks/darks_0gain_300s_2020-03-13/Dark_0001.fits",
		suffixSet("darks/darks_0gain_300s/Dark_0001.fits", "2020-03-13"))
	assert.Equal(t, "darks/set/f.fits", suffixSet("darks/set/f.fits", ""), "no date → unchanged")
}

// mapFrameType folds the inspector's frame types into the coarse s3layout classes (darkflat→flat, video→light).
func TestMapFrameType(t *testing.T) {
	assert.Equal(t, s3layout.Light, mapFrameType(inspect.Light))
	assert.Equal(t, s3layout.Light, mapFrameType(inspect.Video))
	assert.Equal(t, s3layout.Dark, mapFrameType(inspect.Dark))
	assert.Equal(t, s3layout.Bias, mapFrameType(inspect.Bias))
	assert.Equal(t, s3layout.Flat, mapFrameType(inspect.Flat))
	assert.Equal(t, s3layout.Flat, mapFrameType(inspect.DarkFlat))
	assert.Equal(t, s3layout.Unknown, mapFrameType(inspect.Unknown))
}
