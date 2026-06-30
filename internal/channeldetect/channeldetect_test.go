package channeldetect

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fingerprints for typical broadband/narrowband filters (L brightest, Ha faintest).
var fpFor = map[string]Fingerprint{
	"L":  {Background: 500, Flux: 2000, StarRichness: 0.050, ExposureMs: 30000},
	"R":  {Background: 300, Flux: 800, StarRichness: 0.020, ExposureMs: 30000},
	"G":  {Background: 280, Flux: 900, StarRichness: 0.025, ExposureMs: 30000},
	"B":  {Background: 350, Flux: 700, StarRichness: 0.018, ExposureMs: 30000},
	"Ha": {Background: 120, Flux: 150, StarRichness: 0.002, ExposureMs: 30000},
}

// build emits block-captured samples: each block is `perBlock` frames of one filter at a 31 s
// cadence, with a 200 s gap (a wheel move) between blocks.
func build(filters []string, perBlock int) []Sample {
	const cadence, gap = int64(31000), int64(200000)
	var out []Sample
	var t int64
	idx := 0
	for bi, f := range filters {
		if bi > 0 {
			t += gap
		}
		for k := 0; k < perBlock; k++ {
			out = append(out, Sample{Order: t, Path: fmt.Sprintf("f%03d.fits", idx), FP: fpFor[f]})
			t += cadence
			idx++
		}
	}
	return out
}

func filtersByPath(res Result) map[string]string {
	m := make(map[string]string, len(res.Assignments))
	for _, a := range res.Assignments {
		m[a.Path] = a.Filter
	}
	return m
}

func TestDetect_AssignsCyclicOrder(t *testing.T) {
	tests := []struct {
		name     string
		sequence []string // the wheel blocks actually captured, in order
		anchors  bool     // L+Ha present → exact labels expected
	}{
		{"clean LRGBHa", []string{"L", "R", "G", "B", "Ha"}, true},
		{"two full cycles", []string{"L", "R", "G", "B", "Ha", "L", "R", "G", "B", "Ha"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Detect(build(tt.sequence, 6), DefaultOptions())
			require.Len(t, res.Runs, len(tt.sequence))
			byPath := filtersByPath(res)
			samples := build(tt.sequence, 6)
			// every frame of block i must carry the expected filter for that block
			for i, s := range samples {
				want := tt.sequence[i/6]
				assert.Equalf(t, want, byPath[s.Path], "frame %d", i)
			}
			assert.Greater(t, res.OverallConfidence, 0.3)
		})
	}
}

func TestDetect_SkippedFilter_AnchorsHold(t *testing.T) {
	// G is skipped: L,R,B,Ha. R/G/B are indistinguishable by signal, so we only assert the
	// signal-anchored ends (L brightest, Ha faintest) and that the middle stays broadband.
	res := Detect(build([]string{"L", "R", "B", "Ha"}, 6), DefaultOptions())
	require.Len(t, res.Runs, 4)
	assert.Equal(t, "L", res.Runs[0].Filter, "brightest block → L")
	assert.Equal(t, "Ha", res.Runs[3].Filter, "faintest block → Ha")
	for _, r := range res.Runs[1:3] {
		assert.Contains(t, []string{"R", "G", "B"}, r.Filter, "middle blocks are broadband")
	}
}

func TestDetect_FlagsOffBrightnessFirstFrame(t *testing.T) {
	opts := DefaultOptions()
	// one filter block; the first frame is twice as bright (wheel still moving), the rest settled.
	samples := make([]Sample, 6)
	var t0 int64
	for i := range samples {
		bg := 300.0
		if i == 0 {
			bg = 600.0
		}
		samples[i] = Sample{Order: t0, Path: fmt.Sprintf("f%03d.fits", i),
			FP: Fingerprint{Background: bg, Flux: 800, StarRichness: 0.02, ExposureMs: 30000}}
		t0 += 31000
	}
	res := Detect(samples, opts)
	require.Len(t, res.Runs, 1, "should be one run, not split by the bright first frame")

	flagged := map[string]bool{}
	for _, a := range res.Assignments {
		flagged[a.Path] = a.WheelTransition
	}
	assert.True(t, flagged["f000.fits"], "off-brightness first frame is flagged")
	for i := 1; i < 6; i++ {
		assert.False(t, flagged[fmt.Sprintf("f%03d.fits", i)], "settled frames are not flagged")
	}
}

func TestDetect_KeepsSettledFirstFrame(t *testing.T) {
	// first frame only marginally different → NOT flagged ("often, not always").
	samples := make([]Sample, 6)
	var t0 int64
	for i := range samples {
		bg := 300.0
		if i == 0 {
			bg = 305.0
		}
		samples[i] = Sample{Order: t0, Path: fmt.Sprintf("f%03d.fits", i),
			FP: Fingerprint{Background: bg, Flux: 800, StarRichness: 0.02, ExposureMs: 30000}}
		t0 += 31000
	}
	res := Detect(samples, DefaultOptions())
	for _, a := range res.Assignments {
		assert.False(t, a.WheelTransition, a.Path)
	}
}

func TestDetect_Empty(t *testing.T) {
	res := Detect(nil, DefaultOptions())
	assert.Empty(t, res.Assignments)
	assert.Equal(t, DefaultOptions().Order, res.Order)
}

// emissionFor must veto narrowband for star-rich runs: a faint run is only cheaply Ha when stars are
// absent too, so a faint-but-star-rich (genuine broadband) run cannot avalanche to Ha. When star
// richness carries no information (neutral 0.5), it reduces to brightness — preserving prior behavior.
func TestEmissionFor_StarRichnessVetoesNarrowband(t *testing.T) {
	// faint + star-poor → cheap narrowband
	assert.InDelta(t, 0.0, emissionFor("Ha", 0.0, 0.0), 1e-9)
	// faint + star-RICH → narrowband is dearer than broadband, so the run stays broadband
	assert.Greater(t, emissionFor("Ha", 0.0, 1.0), emissionFor("R", 0.0, 1.0),
		"a faint but star-rich run must prefer broadband over Ha")
	// neutral (uninformative) richness → no penalty, narrowband cost is just the brightness blend
	assert.InDelta(t, 0.5*0.3+0.5*0.5, emissionFor("Ha", 0.3, 0.5), 1e-9)
}
