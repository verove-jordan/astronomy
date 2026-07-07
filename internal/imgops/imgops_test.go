package imgops

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPercentile(t *testing.T) {
	tests := []struct {
		name string
		vals []float32
		p    float64
		want float64
	}{
		{"empty", nil, 50, 0},
		{"single", []float32{7}, 50, 7},
		{"min", []float32{1, 2, 3, 4}, 0, 1},
		{"max", []float32{1, 2, 3, 4}, 100, 4},
		{"median even", []float32{1, 2, 3, 4}, 50, 2.5},
		{"median odd", []float32{1, 2, 3, 4, 5}, 50, 3},
		{"interp", []float32{0, 10}, 25, 2.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, Percentile(tt.vals, tt.p), 1e-9)
		})
	}
}

func TestPercentile_DoesNotMutate(t *testing.T) {
	in := []float32{5, 1, 4, 2, 3}
	_ = Percentile(in, 50)
	assert.Equal(t, []float32{5, 1, 4, 2, 3}, in)
}

func TestSubsample(t *testing.T) {
	tests := []struct {
		name    string
		len     int
		n       int
		wantLen int
	}{
		{"shorter than n returns input", 10, 100, 10},
		{"exactly n", 50, 50, 50},
		{"downsamples", 1000, 100, 100},
		{"n zero returns input", 10, 0, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := make([]float32, tt.len)
			for i := range in {
				in[i] = float32(i)
			}
			got := Subsample(in, tt.n)
			assert.LessOrEqual(t, len(got), tt.wantLen+1)
			if tt.n <= 0 || tt.len <= tt.n {
				assert.Equal(t, tt.len, len(got))
			}
		})
	}
}

func TestReflectIndex(t *testing.T) {
	tests := []struct {
		i, n, want int
	}{
		{0, 5, 0}, {4, 5, 4}, {-1, 5, 0}, {-2, 5, 1},
		{5, 5, 4}, {6, 5, 3}, {0, 1, 0}, {3, 1, 0},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ReflectIndex(tt.i, tt.n), "ReflectIndex(%d,%d)", tt.i, tt.n)
	}
}

func TestGaussianBlur_PreservesMeanAndSmooths(t *testing.T) {
	w, h := 32, 32
	src := make([]float32, w*h)
	// A single bright spike in the centre.
	src[16*w+16] = 100
	out := GaussianBlur(src, w, h, 3)

	var sumIn, sumOut float64
	for i := range src {
		sumIn += float64(src[i])
		sumOut += float64(out[i])
	}
	assert.InDelta(t, sumIn, sumOut, 0.5, "box blur conserves total signal (edge-clamped, spike is central)")
	assert.Less(t, out[16*w+16], float32(100), "spike is spread out")
	assert.Greater(t, out[16*w+17], float32(0), "energy leaked to neighbour")
}

func TestGaussianBlur_ZeroSigmaCopies(t *testing.T) {
	src := []float32{1, 2, 3, 4}
	out := GaussianBlur(src, 2, 2, 0)
	assert.Equal(t, src, out)
	out[0] = 99
	assert.Equal(t, float32(1), src[0], "output is a copy, not aliased")
}

func TestMedianFilter_RemovesHotPixel(t *testing.T) {
	w, h := 5, 5
	src := make([]float32, w*h)
	for i := range src {
		src[i] = 10
	}
	src[2*w+2] = 1000 // hot pixel
	out := MedianFilter(src, w, h, 3)
	assert.Equal(t, float32(10), out[2*w+2], "median filter erases isolated hot pixel")
}

func TestBinaryDilation(t *testing.T) {
	w, h := 5, 5
	mask := make([]bool, w*h)
	mask[2*w+2] = true
	got := BinaryDilation(mask, w, h, 1)
	// 4-connected cross around centre.
	want := map[int]bool{2*w + 2: true, 2*w + 1: true, 2*w + 3: true, 1*w + 2: true, 3*w + 2: true}
	for i := range got {
		assert.Equal(t, want[i], got[i], "pixel %d", i)
	}
	assert.False(t, mask[2*w+1], "input not mutated")
}

func TestLabel_CountsComponents(t *testing.T) {
	w, h := 5, 5
	mask := make([]bool, w*h)
	// Component A: top-left 2x1.
	mask[0] = true
	mask[1] = true
	// Component B: bottom-right single, diagonally separate.
	mask[4*w+4] = true
	labels, n := Label(mask, w, h)
	require.Equal(t, 2, n)
	assert.Equal(t, labels[0], labels[1], "adjacent pixels share a label")
	assert.NotEqual(t, labels[0], labels[4*w+4], "separate components differ")
	assert.Equal(t, 0, labels[2*w+2], "background is 0")
}

func TestLabel_UShapeIsOneComponent(t *testing.T) {
	// A U shape needs the union-find equivalence merge to be one component.
	w, h := 5, 5
	mask := make([]bool, w*h)
	set := func(x, y int) { mask[y*w+x] = true }
	for y := 0; y < 4; y++ { // two vertical bars
		set(0, y)
		set(4, y)
	}
	for x := 0; x < 5; x++ { // bottom bar joining them
		set(x, 4)
	}
	_, n := Label(mask, w, h)
	assert.Equal(t, 1, n)
}

func TestGaussianBlur_MatchesWideSigmaMonotone(t *testing.T) {
	// A step edge blurred with a wider sigma should have a gentler transition.
	w, h := 64, 1
	src := make([]float32, w*h)
	for x := 0; x < w; x++ {
		if x >= w/2 {
			src[x] = 1
		}
	}
	narrow := GaussianBlur(src, w, h, 2)
	wide := GaussianBlur(src, w, h, 6)
	edge := w / 2
	assert.Greater(t, math.Abs(float64(narrow[edge]-narrow[edge-4])),
		math.Abs(float64(wide[edge]-wide[edge-4])), "wider sigma → gentler local slope")
}
