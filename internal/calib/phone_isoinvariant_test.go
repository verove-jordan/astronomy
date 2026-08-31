package calib

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMatchPhoneCalibration_ISOInvariant covers the case that made a real session uncalibrable.
//
// A phone on auto-exposure picks its own ISO for every frame. On the 2026-08-11 session the lights
// came out at ISO 2500 and the darks — same tripod, same exposure, minutes later, lens capped — at
// ISO 6400 to 10000. Keyed on exact ISO, every one of them is refused. But those files are linear
// DNGs, whose gain is already normalised, so ISO no longer describes the sensor state at all.
func TestMatchPhoneCalibration_ISOInvariant(t *testing.T) {
	const model = "iPhone 16 Pro"
	masters := []PhoneMaster{
		{Type: MasterDark, ISO: 6400, ExposureMs: 10000, CameraModel: model, Width: 4032, Height: 3024, FrameCount: 12, Path: "dark.fits"},
	}

	tests := []struct {
		name      string
		light     PhoneKey
		wantMatch bool
	}{
		{
			name:      "gain-normalised raw ignores the ISO difference",
			light:     PhoneKey{CameraModel: model, ISO: 2500, ExposureMs: 10000, Width: 4032, Height: 3024, ISOInvariant: true},
			wantMatch: true,
		},
		{
			name: "an ordinary raw still requires the ISO to match",
			// Where gain is NOT normalised the dark signal really does scale with it, so the strict
			// rule has to stay for everything that is not a linear DNG.
			light:     PhoneKey{CameraModel: model, ISO: 2500, ExposureMs: 10000, Width: 4032, Height: 3024},
			wantMatch: false,
		},
		{
			name:      "exposure must still match, since dark current scales with time",
			light:     PhoneKey{CameraModel: model, ISO: 2500, ExposureMs: 7500, Width: 4032, Height: 3024, ISOInvariant: true},
			wantMatch: false,
		},
		{
			name:      "dimensions must still match, whatever the ISO",
			light:     PhoneKey{CameraModel: model, ISO: 2500, ExposureMs: 10000, Width: 8064, Height: 6048, ISOInvariant: true},
			wantMatch: false,
		},
		{
			name:      "a different camera is never a match",
			light:     PhoneKey{CameraModel: "Pixel 9", ISO: 2500, ExposureMs: 10000, Width: 4032, Height: 3024, ISOInvariant: true},
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel := MatchPhoneCalibration(tt.light, masters)

			if !tt.wantMatch {
				assert.Nil(t, sel.Dark, "expected no dark master")
				return
			}
			require.NotNil(t, sel.Dark, "expected the dark master to be selected")
			assert.Equal(t, "dark.fits", sel.Dark.Path)
		})
	}
}
