package calib

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

func library() []Master {
	return []Master{
		{Type: MasterBias, Gain: 139, Offset: 21, Bin: 1, Path: "bias.fits", FrameCount: 8},
		{Type: MasterDark, ExposureMs: 120000, Gain: 139, Offset: 21, Bin: 1, TempMilliC: -15000, Path: "dark.fits", FrameCount: 5},
		{Type: MasterFlat, Filter: "L", ExposureMs: 2000, Gain: 139, Offset: 21, Bin: 1, Path: "flatL.fits", FrameCount: 4},
	}
}

func calSet(t inspect.FrameType, filter string, exp, gain int64, tempC int) inspect.Set {
	return inspect.Set{Key: inspect.SetKey{Type: t, Filter: filter, ExposureMs: exp, Gain: gain, Offset: 21, Bin: 1, TempBucket: tempC}}
}

func TestFindExisting(t *testing.T) {
	lib := library()
	cases := []struct {
		name string
		set  inspect.Set
		want string // matched path, "" for no match
	}{
		{"dark exact", calSet(inspect.Dark, "", 120000, 139, -15), "dark.fits"},
		{"dark within temp tol", calSet(inspect.Dark, "", 120000, 139, -18), "dark.fits"},
		{"dark outside temp tol", calSet(inspect.Dark, "", 120000, 139, -25), ""},
		{"dark wrong exposure", calSet(inspect.Dark, "", 300000, 139, -15), ""},
		{"bias ignores exposure/temp", calSet(inspect.Bias, "", 0, 139, 0), "bias.fits"},
		{"bias wrong gain", calSet(inspect.Bias, "", 0, 200, 0), ""},
		{"flat by filter", calSet(inspect.Flat, "L", 2000, 139, -15), "flatL.fits"},
		{"flat wrong filter", calSet(inspect.Flat, "R", 2000, 139, -15), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := findExisting(lib, tc.set)
			if tc.want == "" {
				assert.Nil(t, m)
				return
			}
			if assert.NotNil(t, m) {
				assert.Equal(t, tc.want, m.Path)
			}
		})
	}
}
