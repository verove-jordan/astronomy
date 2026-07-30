package transient

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// coreFrame is a memFrame with a small "galaxy core" square whose value is coreVal — the multi-
// night saturation fixtures plant a clipped plateau (at the frame's ceiling) or the true core.
func coreFrame(frame int, coreVal float32) *fits.Image {
	im := memFrame(frame, 0)
	for y := 100; y < 108; y++ {
		for x := 200; x < 208; x++ {
			im.Pix[0][y*seqW+x] = coreVal
		}
	}
	return im
}

func corePixel(im *fits.Image) float32 { return im.Pix[0][104*seqW+204] }

// TestMaskCrossFrameValidated_RepairsSaturatedMajorityCore pins the real task #354 L shape: MOST
// frames carry a clipped plateau (so the plain cross-frame median is itself saturated) and the
// repair must draw on the clean minority's median instead.
func TestMaskCrossFrameValidated_RepairsSaturatedMajorityCore(t *testing.T) {
	const trueCore = float32(0.55)
	frames := make([]*fits.Image, 12)
	satCeil := make([]float32, 12)
	for i := range frames {
		if i < 7 { // the saturated nights: plateau at ~their ceiling
			frames[i] = coreFrame(i, 0.99)
			satCeil[i] = 1.0
		} else { // 5 clean frames — exactly minCleanSamples
			frames[i] = coreFrame(i, trueCore)
			satCeil[i] = 1.0
		}
	}
	rep, err := MaskCrossFrameValidated(frames, 0, satCeil) // saturation-only invocation
	require.NoError(t, err)

	s := rep.Summary()
	assert.Equal(t, 7*64, s.SatMaskedPx, "every plateau pixel of every saturated frame is repaired")
	for i := 0; i < 7; i++ {
		assert.InDelta(t, trueCore, corePixel(frames[i]), 1e-4, "frame %d core restored from the clean minority", i)
	}
	for i := 7; i < 12; i++ {
		assert.Equal(t, trueCore, corePixel(frames[i]), "clean frame %d untouched", i)
	}
}

// TestMaskCrossFrameValidated_SaturationGuards pins the do-no-harm rules of the repair.
func TestMaskCrossFrameValidated_SaturationGuards(t *testing.T) {
	tests := []struct {
		name     string
		coreVal  func(i int) float32
		ceil     func(i int) float32
		wantSat  int
		wantCore func(i int) float32
	}{
		{
			name:    "plateau in ALL frames is untouched (no true value exists)",
			coreVal: func(int) float32 { return 0.99 },
			ceil:    func(int) float32 { return 1.0 },
			wantSat: 0, wantCore: func(int) float32 { return 0.99 },
		},
		{
			name:    "below-ceiling bright core is untouched",
			coreVal: func(int) float32 { return 0.85 },
			ceil:    func(int) float32 { return 1.0 }, // threshold 0.90 > 0.85
			wantSat: 0, wantCore: func(int) float32 { return 0.85 },
		},
		{
			name: "fewer than minCleanSamples clean frames leaves the plateau",
			coreVal: func(i int) float32 {
				if i < 9 {
					return 0.99
				}
				return 0.55 // only 3 clean — below the floor of 5
			},
			ceil:    func(int) float32 { return 1.0 },
			wantSat: 0,
			wantCore: func(i int) float32 {
				if i < 9 {
					return 0.99
				}
				return 0.55
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := make([]*fits.Image, 12)
			satCeil := make([]float32, 12)
			for i := range frames {
				frames[i] = coreFrame(i, tt.coreVal(i))
				satCeil[i] = tt.ceil(i)
			}
			rep, err := MaskCrossFrameValidated(frames, 0, satCeil)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSat, rep.Summary().SatMaskedPx)
			for i := range frames {
				assert.Equal(t, tt.wantCore(i), corePixel(frames[i]), "frame %d", i)
			}
		})
	}
}

// TestMaskCrossFrameValidated_PerGroupCeilings: a scaled-down night's plateau sits at ITS OWN
// post-transform ceiling — detection must be per-frame, not global.
func TestMaskCrossFrameValidated_PerGroupCeilings(t *testing.T) {
	const trueCore = float32(0.30)
	frames := make([]*fits.Image, 12)
	satCeil := make([]float32, 12)
	for i := range frames {
		switch {
		case i < 4: // night A: unscaled, plateau near 1.0
			frames[i] = coreFrame(i, 0.99)
			satCeil[i] = 1.0
		case i < 7: // night B: scaled ×0.5 — its plateau lands at ~0.495
			frames[i] = coreFrame(i, 0.495)
			satCeil[i] = 0.5
		default: // 5 clean frames see the true core well below every threshold
			frames[i] = coreFrame(i, trueCore)
			satCeil[i] = 1.0
		}
	}
	rep, err := MaskCrossFrameValidated(frames, 0, satCeil)
	require.NoError(t, err)

	assert.Equal(t, 7*64, rep.Summary().SatMaskedPx, "both nights' plateaus detected at their own ceilings")
	for i := 0; i < 7; i++ {
		assert.InDelta(t, trueCore, corePixel(frames[i]), 1e-4, "frame %d", i)
	}
}

// TestMaskCrossFrameStreamed_SaturationMatchesInMemoryOnFullBasis: the streamed repair (clean
// medians from the basis, per-frame repair as streamed) must reproduce the in-memory pass bit for
// bit when the basis covers every frame — including the trail passes running after it.
func TestMaskCrossFrameStreamed_SaturationMatchesInMemoryOnFullBasis(t *testing.T) {
	const n = 12
	coreVal := func(i int) float32 {
		if i < 7 {
			return 0.99
		}
		return 0.55
	}
	satCeil := make([]float32, n)
	mem := make([]*fits.Image, n)
	dir := t.TempDir()
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		satCeil[i] = 1.0
		mem[i] = coreFrame(i, coreVal(i))
		p := filepath.Join(dir, fmt.Sprintf("r_light_%05d.fits", i+1))
		require.NoError(t, coreFrame(i, coreVal(i)).WriteFITS(p))
		paths[i] = p
	}

	memRep, err := MaskCrossFrameValidated(mem, 3.0, satCeil)
	require.NoError(t, err)
	strRep, err := MaskCrossFrameStreamed(paths, 3.0, n, satCeil)
	require.NoError(t, err)

	assert.Equal(t, memRep.Summary(), strRep.Summary())
	for i, p := range paths {
		onDisk, err := fits.ReadImage(p)
		require.NoError(t, err)
		assert.Equal(t, mem[i].Pix[0], onDisk.Pix[0], "frame %d pixels must match bit for bit", i)
	}
}
