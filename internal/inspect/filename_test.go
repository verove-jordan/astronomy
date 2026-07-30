package inspect

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseFilenameMeta_ASI(t *testing.T) {
	cases := []struct {
		path string
		want fileMeta
	}{
		{
			"input/galaxy/L/Light_ASIImg_30sec_Bin1_filter-L_-20.0C_gain300_2025-03-27_232804_frame0001.fit",
			fileMeta{Type: Light, Filter: "L", ExposureMs: 30000, Gain: 300, TempMilliC: -20000, HasTemp: true, Bin: 1},
		},
		{
			"input/galaxy/Darks/Dark_ASIImg_30sec_Bin1_filter-B_-20.0C_gain300_2025-03-28_025415_frame0001.fit",
			fileMeta{Type: Dark, Filter: "B", ExposureMs: 30000, Gain: 300, TempMilliC: -20000, HasTemp: true, Bin: 1},
		},
		{
			"input/galaxy/Bias/Bias_ASIImg_0.001sec_Bin1_filter-L_-19.5C_gain300_2025-03-28_023107_frame0021.fit",
			fileMeta{Type: Bias, Filter: "L", ExposureMs: 1, Gain: 300, TempMilliC: -19500, HasTemp: true, Bin: 1},
		},
		{
			"input/galaxy/Flats/Ha/Flat_ASIImg_0.2sec_Bin1_filter-Ha_-20.0C_gain300_2025-03-28_031453_frame0001.fit",
			fileMeta{Type: Flat, Filter: "Ha", ExposureMs: 200, Gain: 300, TempMilliC: -20000, HasTemp: true, Bin: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := parseFilenameMeta(tc.path)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFilterFromDirs(t *testing.T) {
	// Lights stored in per-filter folders with no filter token in the name.
	assert.Equal(t, "G", parseFilenameMeta("input/galaxy/G/Light_30sec_frame1.fit").Filter)
	assert.Equal(t, Dark, parseFilenameMeta("input/galaxy/Darks/something_30sec.fit").Type)
}

func TestTypeFromDirs_Compound(t *testing.T) {
	cases := []struct {
		path string
		want FrameType
	}{
		{"input/2020_05_06/darks_0gain_300s_-25deg/autorun/x.fit", Dark},
		{"input/C2019/offset_-15_250gain/autorun/x.fit", Bias},
		{"input/2020_05_06/flats_0gain_Ha/autorun/x.fit", Flat},
		{"input/M27/offsets/2019-08-30_00_23/x.fit", Bias},
		{"input/M27/darks/2019-08-30_00_02/x.fit", Dark},
		{"input/sess/master_flats/x.fit", Flat},
		{"input/sess/dark_flats/x.fit", DarkFlat},
		{"input/sess/darkstar_nebula/L/x.fit", Unknown},        // "darkstar" is not the word "dark"
		{"input/M27/m27/data/2019-08-29_22_20/x.fit", Unknown}, // a light folder names no type
		// SharpCap nests its "CapObj" capture folder UNDER the type folder (.../darks/CapObj/<session>/).
		// The explicit calibration grandparent must win over the nearer CapObj (which else reads as Light).
		{"input/2023_02_27/darks/CapObj/2023-02-28_05_18_38Z/x.FIT", Dark},
		{"input/2023_02_27/offset/CapObj/2023-02-28_05_52_24Z/x.FIT", Bias},
		{"input/2023_02_27/flats/CapObj/2023-02-28_06_01_48Z/x.FIT", Flat},
		// A bare CapObj object folder with no calibration ancestor is still an unlabeled light (fallback).
		{"input/2023_02_27/triplet_m66/CapObj/2023-02-27_22_55_39Z/x.FIT", Light},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, typeFromDirs(tc.path))
		})
	}
}

func TestFilterFromDirs_Robust(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"input/m33/L/2019/x.fit", "L"},
		{"input/M81_M82_2019/M81M82/V/2019/x.fit", "G"}, // Johnson V → green channel
		{"input/M81_M82_2019/M81M82/R/2019/x.fit", "R"},
		{"input/sess/Red/x.fit", "R"},
		{"input/sess/Ha_300s/x.fit", "Ha"},
		{"input/sess/R band/x.fit", "R"},
		{"input/M27/m27/data/2019/x.fit", ""}, // non-filter folder
		// Compound session name that merely mentions a filter must NOT be read as that filter.
		{"input/2020_05_06/m81_m82_LRGB_0gain_Ha_180gain_120s/autorun/x.fit", ""},
		// A calibration folder states its purpose, so a filter qualifier anywhere in it is intentional.
		{"input/ngc6992/flats_0gain_Ha/autorun/x.fit", "Ha"},
		{"input/sess/darks_0gain_300s/x.fit", ""}, // calibration folder with no filter qualifier
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, filterFromDirs(tc.path))
		})
	}
}

func TestGainFromDirs(t *testing.T) {
	cases := []struct {
		path string
		want int64
	}{
		// The bug: "300" belongs to the exposure token, not the "0gain" gain → gain is 0, not 300.
		{"input/2020/darks_0gain_300s_-25deg/autorun/x.fit", 0},
		{"input/C2019/offset_-15_250gain/autorun/x.fit", 250}, // legacy "<n>gain" suffix form
		{"input/sess/flats_0gain_Ha/x.fit", 0},
		{"input/sess/Light_gain300_30sec/x.fit", 300}, // glued "gain<n>" prefix form
		{"input/sess/nogainhere/x.fit", 0},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, gainFromDirs(tc.path))
		})
	}
}

func TestWheelSlotFromFilename(t *testing.T) {
	cases := []struct {
		path string
		want int
	}{
		{"input/M33/CapObj/2021-08-14_00_47/2021-08-14-0047_6-1-CapObj_0000.FIT", 1},
		{"input/M27/data/2019/2019-08-29-2220_5-2-L_0000.FIT", 2},
		{"input/C2019/autorun/2020-04-13-2239_3-4-autorun_0018.FIT", 4},
		{"input/galaxy/L/Light_ASIImg_30sec_filter-L_frame0001.fit", 0}, // ordinary name: no slot
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, parseFilenameMeta(tc.path).WheelSlot)
		})
	}
}
