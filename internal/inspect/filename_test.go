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
