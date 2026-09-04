package inspect

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// How a set's temperature is decided.
//
// This used to round each frame to the nearest 5 °C. A fixed grid has hard edges at -7.5 and -12.5,
// so a cooler regulating at -9.8 with a couple of degrees of excursion landed on both sides of both:
// ONE run of darks was reported as -5, -15 and "a few" -10, and the stragglers — sets of one or two
// frames — were too small to build a master and were silently lost.

// tempFrame is an in-memory frame at a given sensor temperature.
func tempFrame(typ FrameType, tempC float64, i int) *Frame {
	return &Frame{
		Path: fmt.Sprintf("/f/%s_%03d.fit", typ, i), Type: typ,
		ExposureMs: 60000, Gain: 100, Offset: 50, BinX: 1, BinY: 1,
		TempMilliC: int64(tempC * 1000), HasTemp: true,
	}
}

func framesAt(typ FrameType, temps ...float64) []*Frame {
	out := make([]*Frame, 0, len(temps))
	for i, t := range temps {
		out = append(out, tempFrame(typ, t, i))
	}
	return out
}

// setShape reports each set as "count@bucket", in the order the report shows them.
func setShape(sets []Set) []string {
	out := make([]string, 0, len(sets))
	for _, s := range sets {
		out = append(out, fmt.Sprintf("%d@%d", s.Count, s.Key.TempBucket))
	}
	return out
}

func TestBuildSets_Temperature(t *testing.T) {
	tests := []struct {
		name  string
		typ   FrameType
		temps []float64
		want  []string
	}{
		{
			name: "a cooler regulating at -9.8 stays ONE set across both grid edges",
			typ:  Dark,
			// The reported bug: the 5 °C grid put these in -5, -10 and -15.
			temps: []float64{-9.8, -7.4, -12.4, -9.9, -10.1, -7.6, -12.2},
			want:  []string{"7@-10"},
		},
		{
			name:  "sub-degree drift never splits",
			typ:   Dark,
			temps: []float64{-19.5, -20.0, -20.5, -19.8, -20.2},
			want:  []string{"5@-20"},
		},
		{
			name: "a cooler still ramping leaves its settling frames behind",
			typ:  Dark,
			// Measured from a real 30-frame dark run: the first frames were taken on the way down.
			temps: []float64{-20.3, -20.1, -20.0, -16.3, -12.3, -10.3, -10.0, -10.0, -9.4},
			want:  []string{"3@-20", "1@-16", "5@-10"},
		},
		{
			name: "a long slow ramp is capped rather than chained into one master",
			typ:  Dark,
			// Every gap is small, so a pure nearest-neighbour rule would merge -20 into a -10 master.
			temps: []float64{-20, -19, -18, -17, -16, -15, -14, -13, -12, -11, -10},
			want:  []string{"6@-17", "5@-12"},
		},
		{
			name:  "genuinely different set points stay apart",
			typ:   Dark,
			temps: []float64{-20.1, -20.0, -19.9, -10.1, -10.0, -9.9},
			want:  []string{"3@-20", "3@-10"},
		},
		{
			name: "flats are not grouped by temperature at all",
			typ:  Flat,
			// Measured: 50 real flats taken while the sensor warmed from -8.7 to +16.1 became SIX
			// sets, so a flat master could never use more than a fifth of them.
			temps: []float64{-8.7, -5.0, 0.0, 5.0, 10.0, 16.1},
			want:  []string{"6@0"},
		},
		{
			name:  "bias has no temperature, as before",
			typ:   Bias,
			temps: []float64{-17.6, -15.0, -14.0},
			want:  []string{"3@0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, setShape(buildSets(framesAt(tt.typ, tt.temps...))))
		})
	}
}

// A frame that recorded no temperature is the ABSENCE of a measurement, not a measurement of zero,
// so it must not be pooled with frames that happen to sit near 0 °C.
func TestBuildSets_FramesWithoutATemperatureStayApart(t *testing.T) {
	frames := framesAt(Dark, -0.2, 0.1)
	noTemp := tempFrame(Dark, 0, 99)
	noTemp.TempMilliC, noTemp.HasTemp = 0, false

	sets := buildSets(append(frames, noTemp))

	require.Len(t, sets, 2)
	counts := map[int]int{}
	for _, s := range sets {
		counts[s.Key.TempBucket] += s.Count
	}
	assert.Equal(t, map[int]int{0: 3}, counts, "both land on bucket 0 but must be two separate sets")
}

// The frames of a set come back in a stable order, not the temperature order the clustering needed.
func TestBuildSets_ClusterKeepsFramesInAStableOrder(t *testing.T) {
	sets := buildSets(framesAt(Dark, -10.2, -9.8, -10.0))
	require.Len(t, sets, 1)

	paths := make([]string, 0, 3)
	for _, fr := range sets[0].Frames {
		paths = append(paths, fr.Path)
	}
	assert.Equal(t, []string{"/f/DARK_000.fit", "/f/DARK_001.fit", "/f/DARK_002.fit"}, paths)
}

// The label is the MEDIAN, because the frames taken while a cooler settles are outliers by
// definition and the set should say where the run actually sat.
func TestBuildSets_TemperatureLabelIsTheMedian(t *testing.T) {
	sets := buildSets(framesAt(Dark, -12.0, -10.0, -10.0, -10.0, -9.0))
	require.Len(t, sets, 1)
	assert.Equal(t, -10, sets[0].Key.TempBucket)
}
