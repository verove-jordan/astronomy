package s3layout

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A DATE-OBS just after midnight local (03:33 UTC) that groups with the evening before it.
const dateObs20200314 = 1584156780000 // 2020-03-14T03:33:00Z → night of 2020-03-13

func TestClassify_LightsCalibAndFallback(t *testing.T) {
	tests := []struct {
		name      string
		folderRel string
		files     []FileInfo
		wantKeys  map[string]string
	}{
		{
			name:      "compound object + year peeled, DATE-OBS sets night",
			folderRel: "data/M81_M82_2020",
			files: []FileInfo{
				{Rel: "L/light_0001.fits", Type: Light, DateObsMs: dateObs20200314},
				{Rel: "Ha/light_0009.fits", Type: Light, DateObsMs: dateObs20200314},
			},
			wantKeys: map[string]string{
				"L/light_0001.fits":  "lum/M81_M82/2020-03-13/L/light_0001.fits",
				"Ha/light_0009.fits": "lum/M81_M82/2020-03-13/Ha/light_0009.fits",
			},
		},
		{
			name:      "bare object dir, year from DATE-OBS",
			folderRel: "M101",
			files:     []FileInfo{{Rel: "l_0001.fits", Type: Light, DateObsMs: dateObs20200314}},
			wantKeys:  map[string]string{"l_0001.fits": "lum/M101/2020-03-13/l_0001.fits"},
		},
		{
			name:      "verbatim full-date dir + OBJECT-header object",
			folderRel: "2019-08-26_03_33_51Z",
			files:     []FileInfo{{Rel: "x.fits", Type: Light, Object: "NGC 6992", DateObsMs: dateObs20200314}},
			wantKeys:  map[string]string{"x.fits": "lum/NGC_6992/2019-08-26/x.fits"},
		},
		{
			name:      "orion_2019 → object orion, year fallback (no DATE-OBS)",
			folderRel: "orion_2019",
			files:     []FileInfo{{Rel: "o1.fits", Type: Light, MTimeMs: 1}},
			wantKeys:  map[string]string{"o1.fits": "lum/orion/2019/o1.fits"},
		},
		{
			name:      "case normalization + counter peel",
			folderRel: "ngc6960_2",
			files:     []FileInfo{{Rel: "n1.fits", Type: Light, MTimeMs: 1, DateObsMs: dateObs20200314}},
			wantKeys:  map[string]string{"n1.fits": "lum/NGC6960/2020-03-13/n1.fits"},
		},
		{
			name:      "signature dark set kept verbatim",
			folderRel: "data/darks_0gain_300s_-25deg",
			files:     []FileInfo{{Rel: "Dark_0001.fits", Type: Dark, MTimeMs: 1}},
			wantKeys:  map[string]string{"Dark_0001.fits": "darks/darks_0gain_300s_-25deg/Dark_0001.fits"},
		},
		{
			name:      "generic darks dir gets the date appended",
			folderRel: "M101",
			files:     []FileInfo{{Rel: "darks/d1.fits", Type: Dark, DateObsMs: 0, MTimeMs: 1600000000000}},
			wantKeys:  map[string]string{"darks/d1.fits": "darks/darks_2020-09-13/d1.fits"},
		},
		{
			name:      "bias → offsets, flat → flats",
			folderRel: "offset_-15_250gain",
			files: []FileInfo{
				{Rel: "b1.fits", Type: Bias, MTimeMs: 1},
				{Rel: "f1.fits", Type: Flat, MTimeMs: 1},
			},
			wantKeys: map[string]string{
				"b1.fits": "offsets/offset_-15_250gain/b1.fits",
				"f1.fits": "flats/offset_-15_250gain/f1.fits",
			},
		},
		{
			name:      "mixed tree splits across roots; info.txt inherits its dir type",
			folderRel: "M42",
			files: []FileInfo{
				{Rel: "L/l1.fits", Type: Light, DateObsMs: dateObs20200314},
				{Rel: "darks/d1.fits", Type: Dark, MTimeMs: 1},
				{Rel: "darks/info.txt", Type: Unknown, MTimeMs: 1}, // inherits Dark
			},
			wantKeys: map[string]string{
				"L/l1.fits":      "lum/M42/2020-03-13/L/l1.fits",
				"darks/d1.fits":  "darks/darks_2020-03-13/d1.fits",
				"darks/info.txt": "darks/darks_2020-03-13/info.txt",
			},
		},
		{
			name:      "re-import of the canonical layout is idempotent",
			folderRel: "lum/M101/2020-03-01",
			files:     []FileInfo{{Rel: "l1.fits", Type: Light, DateObsMs: dateObs20200314}},
			wantKeys:  map[string]string{"l1.fits": "lum/M101/2020-03-01/l1.fits"},
		},
		{
			name:      "object-less light (all-generic path, no OBJECT header) → legacy data/ fallback",
			folderRel: "input/sub",
			files:     []FileInfo{{Rel: "x.fits", Type: Light}},
			wantKeys:  map[string]string{"x.fits": "data/input/sub/x.fits"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.folderRel, tt.files)
			assert.Equal(t, tt.wantKeys, got.Keys)
		})
	}
}

func TestClassify_UnclassifiableWarns(t *testing.T) {
	got := Classify("input/sub", []FileInfo{{Rel: "x.fits", Type: Light}})
	assert.NotEmpty(t, got.Warnings, "a legacy fallback records a warning")
}

func TestParsePathDate(t *testing.T) {
	cases := map[string]string{
		"2019-08-26":           "2019-08-26",
		"2019-08-26_03_33_51Z": "2019-08-26",
		"2021_08_14":           "2021-08-14",
		"04_04_2020":           "2020-04-04",
		"20260513":             "2026-05-13",
		"M101":                 "",
		"2020":                 "", // a bare year is not a full date here
		"2020-13-40":           "", // out of range
	}
	for in, want := range cases {
		assert.Equal(t, want, parsePathDate(in), in)
	}
}

func TestPeelObject(t *testing.T) {
	cases := map[string]string{
		"M81_M82_2020": "M81_M82",
		"orion_2019":   "orion",
		"moon_2":       "moon",
		"NGC6888":      "NGC6888", // single token — the digits are part of the id, not a year
		"C2019":        "C2019",
		"M31":          "M31",
	}
	for in, want := range cases {
		assert.Equal(t, want, peelObject(in), in)
	}
}
