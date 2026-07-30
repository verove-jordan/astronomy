package grade

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func rejectedSet(metrics []Metric) map[int]string {
	out := map[int]string{}
	for _, m := range metrics {
		if m.Rejected {
			out[m.Index] = m.RejectReason
		}
	}
	return out
}

func TestGrade_RejectsElongated(t *testing.T) {
	m := []Metric{
		{Index: 1, FWHM: 3, Roundness: 0.97, StarCount: 30},
		{Index: 2, FWHM: 3, Roundness: 0.96, StarCount: 30},
		{Index: 3, FWHM: 3, Roundness: 0.97, StarCount: 30},
		{Index: 4, FWHM: 3, Roundness: 0.50, StarCount: 30}, // elongated
	}
	Grade(m, DefaultOptions())
	rej := rejectedSet(m)
	assert.Contains(t, rej, 4)
	assert.Len(t, rej, 1)
}

func TestGrade_KeepsTightSet(t *testing.T) {
	// Near-identical frames must not be rejected just because MAD is tiny.
	m := []Metric{
		{Index: 1, FWHM: 3.12, Roundness: 0.97, StarCount: 30},
		{Index: 2, FWHM: 3.12, Roundness: 0.97, StarCount: 31},
		{Index: 3, FWHM: 3.11, Roundness: 0.97, StarCount: 30},
		{Index: 4, FWHM: 3.13, Roundness: 0.97, StarCount: 29},
	}
	Grade(m, DefaultOptions())
	assert.Empty(t, rejectedSet(m))
}

func TestGrade_RejectsSoftFrame(t *testing.T) {
	m := []Metric{
		{Index: 1, FWHM: 2.0, Roundness: 0.97, StarCount: 30},
		{Index: 2, FWHM: 2.0, Roundness: 0.97, StarCount: 30},
		{Index: 3, FWHM: 2.0, Roundness: 0.97, StarCount: 30},
		{Index: 4, FWHM: 2.0, Roundness: 0.97, StarCount: 30},
		{Index: 5, FWHM: 4.0, Roundness: 0.97, StarCount: 30}, // soft
	}
	Grade(m, DefaultOptions())
	assert.Contains(t, rejectedSet(m), 5)
}

func TestGrade_RejectsClouds(t *testing.T) {
	m := []Metric{
		{Index: 1, FWHM: 3, Roundness: 0.97, StarCount: 40},
		{Index: 2, FWHM: 3, Roundness: 0.97, StarCount: 38},
		{Index: 3, FWHM: 3, Roundness: 0.97, StarCount: 41},
		{Index: 4, FWHM: 3, Roundness: 0.97, StarCount: 5}, // clouds
	}
	Grade(m, DefaultOptions())
	assert.Contains(t, rejectedSet(m), 4)
}

func TestGrade_RejectsTrail(t *testing.T) {
	m := []Metric{
		{Index: 1, FWHM: 3, Roundness: 0.97, StarCount: 30},
		{Index: 2, FWHM: 3, Roundness: 0.97, StarCount: 30},
		{Index: 3, FWHM: 3, Roundness: 0.97, StarCount: 30, TrailDetected: true, TrailScore: 0.9},
	}
	Grade(m, DefaultOptions())
	assert.Contains(t, rejectedSet(m), 3)
}

func TestGrade_KeepsStackMinimum(t *testing.T) {
	// Siril's stack needs two frames: even when every frame is flagged, the two sharpest are
	// restored with their original reason kept as provenance.
	m := []Metric{
		{Index: 1, FWHM: 3.0, Roundness: 0.4, StarCount: 30},
		{Index: 2, FWHM: 4.0, Roundness: 0.3, StarCount: 30},
		{Index: 3, FWHM: 5.0, Roundness: 0.3, StarCount: 30},
	}
	Grade(m, DefaultOptions())
	assert.Len(t, rejectedSet(m), 1, "only the worst frame stays rejected")
	assert.Contains(t, rejectedSet(m), 3)
	for _, kept := range []int{0, 1} {
		assert.False(t, m[kept].Rejected)
		assert.Contains(t, m[kept].RejectReason, "kept (stack minimum) — was: ")
	}
}

func TestKeepAtLeast(t *testing.T) {
	tests := []struct {
		name     string
		metrics  []Metric
		n        int
		wantKept []int // indices into metrics expected to survive
	}{
		{
			"restores best rejected up to n",
			[]Metric{
				{Index: 1, FWHM: 2.0, Rejected: true, RejectReason: "soft"},
				{Index: 2, FWHM: 3.0, Rejected: true, RejectReason: "soft"},
				{Index: 3, FWHM: 4.0, Rejected: true, RejectReason: "soft"},
			},
			2,
			[]int{0, 1},
		},
		{
			"lone registered frame caps n",
			[]Metric{
				{Index: 1, FWHM: 2.0, Rejected: true, RejectReason: "trail"},
				{Index: 2, FWHM: 0, Rejected: true, RejectReason: "not registered"},
			},
			2,
			[]int{0},
		},
		{
			"enough survivors is a no-op",
			[]Metric{
				{Index: 1, FWHM: 2.0},
				{Index: 2, FWHM: 3.0},
				{Index: 3, FWHM: 4.0, Rejected: true, RejectReason: "soft"},
			},
			2,
			[]int{0, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepAtLeast(tt.metrics, tt.n)
			var kept []int
			for i, m := range tt.metrics {
				if !m.Rejected {
					kept = append(kept, i)
				}
			}
			assert.Equal(t, tt.wantKept, kept)
		})
	}
}

func TestKeptAndRejectedIndices(t *testing.T) {
	m := []Metric{
		{Index: 1, Rejected: false},
		{Index: 2, Rejected: true},
		{Index: 3, Rejected: false},
	}
	assert.Equal(t, []int{2}, RejectedIndices(m))
	assert.Len(t, Kept(m), 2)
}

// sharpNightSpan builds n sharp frames (the "best night" whose merged-population medians used to
// evict everyone else) starting at 1-based index start.
func sharpNightSpan(start, n int) []Metric {
	out := make([]Metric, n)
	for i := range out {
		out[i] = Metric{Index: start + i, FWHM: 2.5, Roundness: 0.92, StarCount: 120, Background: 0.01}
	}
	return out
}

// softNightSpan builds n genuinely-worse-but-usable frames: rounder-than-floor but far below the
// sharp night's medians on every relative axis.
func softNightSpan(start, n int) []Metric {
	out := make([]Metric, n)
	for i := range out {
		out[i] = Metric{Index: start + i, FWHM: 4.8, Roundness: 0.72, StarCount: 25, Background: 0.03}
	}
	return out
}

// TestGradeGrouped_SoftNightSurvivesPerSpan pins the task #354 fix: graded as ONE population the
// soft night is evicted wholesale by the sharp night's medians ("few stars", "elongated vs
// session"); graded per span each night is judged against itself and both contribute.
func TestGradeGrouped_SoftNightSurvivesPerSpan(t *testing.T) {
	sharp, soft := sharpNightSpan(1, 8), softNightSpan(9, 8)

	pooled := append(append([]Metric{}, sharp...), soft...)
	Grade(pooled, DefaultOptions())
	assert.NotEmpty(t, rejectedSet(pooled), "one population: the soft night is evicted (the old failure)")

	grouped := append(append([]Metric{}, sharp...), soft...)
	GradeGrouped(grouped, []Span{{Start: 0, End: 8}, {Start: 8, End: 16}}, DefaultOptions())
	assert.Empty(t, rejectedSet(grouped), "per-night spans: both nights are judged against themselves")
}

// TestGradeGrouped_AbsoluteFloorsStayGlobal: per-span scoping must not weaken the absolute rules —
// a below-floor roundness or a detected trail is rejected inside ANY span.
func TestGradeGrouped_AbsoluteFloorsStayGlobal(t *testing.T) {
	soft := softNightSpan(1, 6)
	soft = append(soft, Metric{Index: 7, FWHM: 4.8, Roundness: 0.40, StarCount: 25, Background: 0.03})           // below the 0.55 floor
	soft = append(soft, Metric{Index: 8, FWHM: 4.8, Roundness: 0.72, StarCount: 25, TrailDetected: true, TrailScore: 3}) // trailed
	sharp := sharpNightSpan(9, 6)

	all := append(append([]Metric{}, soft...), sharp...)
	GradeGrouped(all, []Span{{Start: 0, End: 8}, {Start: 8, End: 14}}, DefaultOptions())
	rej := rejectedSet(all)
	assert.Contains(t, rej, 7, "the roundness floor fires inside a soft span")
	assert.Contains(t, rej, 8, "trail rejection fires inside a soft span")
	assert.Len(t, rej, 2)
}

// TestGradeGrouped_TinySpanSkipsRelativeRules: a span under minFramesForStats gets no relative
// statistics of its own (nothing meaningful to compare against) — only absolute rules apply.
func TestGradeGrouped_TinySpanSkipsRelativeRules(t *testing.T) {
	tiny := []Metric{
		{Index: 1, FWHM: 9.0, Roundness: 0.70, StarCount: 5, Background: 0.09},
		{Index: 2, FWHM: 2.0, Roundness: 0.95, StarCount: 90, Background: 0.01},
	}
	sharp := sharpNightSpan(3, 8)
	all := append(append([]Metric{}, tiny...), sharp...)
	GradeGrouped(all, []Span{{Start: 0, End: 2}, {Start: 2, End: 10}}, DefaultOptions())
	assert.Empty(t, rejectedSet(all), "a 2-frame night cannot be relative-graded — and is not judged by the other night's medians")
}

// TestGradeGrouped_StackMinimumStaysGlobal: the Siril stack floor is a whole-stack constraint —
// per-span grading must not resurrect frames when OTHER spans already carry enough survivors.
func TestGradeGrouped_StackMinimumStaysGlobal(t *testing.T) {
	// Span 1: all trailed (absolute rule) — legitimately zero survivors.
	bad := []Metric{
		{Index: 1, FWHM: 3, Roundness: 0.9, StarCount: 30, TrailDetected: true, TrailScore: 2},
		{Index: 2, FWHM: 3, Roundness: 0.9, StarCount: 30, TrailDetected: true, TrailScore: 2},
	}
	good := sharpNightSpan(3, 4)
	all := append(append([]Metric{}, bad...), good...)
	GradeGrouped(all, []Span{{Start: 0, End: 2}, {Start: 2, End: 6}}, DefaultOptions())
	rej := rejectedSet(all)
	assert.Len(t, rej, 2, "the trailed night stays rejected — 4 global survivors already satisfy the stack floor")
	assert.Contains(t, rej, 1)
	assert.Contains(t, rej, 2)
}

// TestGradeGrouped_SingleSpanMatchesGrade: one span over everything must be EXACTLY Grade — the
// single-session path's behavior may not drift by construction.
func TestGradeGrouped_SingleSpanMatchesGrade(t *testing.T) {
	mixed := append(append([]Metric{}, sharpNightSpan(1, 5)...), softNightSpan(6, 3)...)
	mixed = append(mixed, Metric{Index: 9, FWHM: 0}) // unregistered

	asGrade := append([]Metric{}, mixed...)
	Grade(asGrade, DefaultOptions())
	asGrouped := append([]Metric{}, mixed...)
	GradeGrouped(asGrouped, []Span{{Start: 0, End: len(asGrouped)}}, DefaultOptions())

	assert.Equal(t, asGrade, asGrouped)
}
