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
		{Index: 2, FWHM: 3, Roundness: 0.97, StarCount: 30, TrailDetected: true, TrailScore: 0.9},
	}
	Grade(m, DefaultOptions())
	assert.Contains(t, rejectedSet(m), 2)
}

func TestGrade_NeverRejectsEverything(t *testing.T) {
	m := []Metric{
		{Index: 1, FWHM: 3.0, Roundness: 0.4, StarCount: 30},
		{Index: 2, FWHM: 4.0, Roundness: 0.3, StarCount: 30},
	}
	Grade(m, DefaultOptions())
	assert.Len(t, rejectedSet(m), 1, "the sharpest frame is kept even if all are flagged")
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
