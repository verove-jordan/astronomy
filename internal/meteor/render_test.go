package meteor

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// frameWithStreak builds a flat sky carrying one bright diagonal trail.
//
// The trail is drawn BROAD — half-width 18 against a measured width of 3 — on purpose. A trail one
// pixel wide would leave the layer zero a pixel off axis simply because there is no light there to
// paint, which says nothing about whether the band's own edge is feathered; the source has to carry
// light past where the band stops for the feather to be visible at all.
func frameWithStreak(w, h int, sky float32) *fits.Image {
	im := fits.NewImage(w, h, 3)
	for c := 0; c < 3; c++ {
		for i := range im.Pix[c] {
			im.Pix[c][i] = sky
		}
	}
	ux, uy := 2/math.Sqrt(5), 1/math.Sqrt(5)
	px, py := -uy, ux
	for s := 0.0; s <= 223; s += 0.25 {
		for q := -18.0; q <= 18; q += 0.5 {
			x := int(math.Round(50 + s*ux + q*px))
			y := int(math.Round(60 + s*uy + q*py))
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			for c := 0; c < 3; c++ {
				im.Pix[c][y*w+x] = sky + 0.3
			}
		}
	}
	return im
}

func theStreak() Streak {
	return Streak{X1: 50, Y1: 60, X2: 250, Y2: 160, LengthPx: 223.6, WidthPx: 3, Frame: 2}
}

func TestRenderLayer_PaintsTheStreakAndNothingElse(t *testing.T) {
	const w, h = 400, 300
	im := frameWithStreak(w, h, 0.05)
	layer, err := RenderLayer(func(int) (*fits.Image, error) { return im, nil }, nil,
		[]Streak{theStreak()}, DefaultRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	if layer == nil {
		t.Fatal("no layer rendered")
	}
	// On the trail the excess must survive; far from it the layer must be exactly zero, or the blend
	// would raise the whole sky.
	mid := layer.Pix[0][110*w+150]
	if mid < 0.2 {
		t.Errorf("the trail was not painted: %.3f at its middle, want about 0.3", mid)
	}
	if far := layer.Pix[0][250*w+350]; far != 0 {
		t.Errorf("the layer is not empty away from the trail: %.4f", far)
	}
	// The sky itself must not come with it.
	if mid > 0.32 {
		t.Errorf("the sky was painted along with the trail: %.3f", mid)
	}
}

func TestRenderLayer_FadesAtTheEdgesRatherThanCuttingOff(t *testing.T) {
	const w, h = 400, 300
	im := frameWithStreak(w, h, 0.05)
	layer, err := RenderLayer(func(int) (*fits.Image, error) { return im, nil }, nil,
		[]Streak{theStreak()}, DefaultRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	// Walk perpendicular to the trail from its centre outwards; the values must not jump to zero.
	ux, uy := 2.0/math.Sqrt(5), 1.0/math.Sqrt(5)
	px, py := -uy, ux
	var prev float32 = -1
	for d := 0.0; d < 20; d++ {
		x, y := int(math.Round(150+px*d)), int(math.Round(110+py*d))
		if x < 0 || y < 0 || x >= w || y >= h {
			break
		}
		v := layer.Pix[0][y*w+x]
		if prev >= 0 && prev > 0.02 && v == 0 {
			t.Fatalf("the layer cuts from %.3f to 0 at %.0f px off axis: the blend would show a hard edge", prev, d)
		}
		prev = v
	}
}

func TestBlend_AddsTheLayerAndLeavesTheRestAlone(t *testing.T) {
	const w, h = 64, 64
	base := fits.NewImage(w, h, 3)
	for c := 0; c < 3; c++ {
		for i := range base.Pix[c] {
			base.Pix[c][i] = 0.1
		}
	}
	layer := fits.NewImage(w, h, 3)
	layer.Pix[0][10*w+10] = 0.4
	got := Blend(base, layer, 0.5)
	if v := got.Pix[0][10*w+10]; math.Abs(float64(v)-0.3) > 1e-6 {
		t.Errorf("blended value %.4f, want 0.30", v)
	}
	if v := got.Pix[0][20*w+20]; math.Abs(float64(v)-0.1) > 1e-6 {
		t.Errorf("untouched pixel changed to %.4f", v)
	}
	// Blend must not modify its inputs — a caller keeps the clean version.
	if base.Pix[0][10*w+10] != 0.1 {
		t.Error("Blend mutated the base image")
	}
}

func TestBlend_IgnoresAMismatchedLayer(t *testing.T) {
	base := fits.NewImage(32, 32, 3)
	for i := range base.Pix[0] {
		base.Pix[0][i] = 0.2
	}
	got := Blend(base, fits.NewImage(16, 16, 3), 1)
	if got.Pix[0][0] != 0.2 {
		t.Error("a layer of the wrong size was blended anyway")
	}
}

// A frame whose shape does not match the canvas must be skipped, not stretched onto it: the meteor
// would land somewhere it never was.
func TestRenderLayer_SkipsAFrameOfAnotherShape(t *testing.T) {
	good := frameWithStreak(400, 300, 0.05)
	odd := frameWithStreak(200, 150, 0.05)
	ss := []Streak{theStreak(), {X1: 20, Y1: 20, X2: 120, Y2: 70, WidthPx: 3, Frame: 5}}
	layer, err := RenderLayer(func(f int) (*fits.Image, error) {
		if f == 5 {
			return odd, nil
		}
		return good, nil
	}, nil, ss, DefaultRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	if layer.W != 400 || layer.H != 300 {
		t.Fatalf("layer is %dx%d, want the first frame's 400x300", layer.W, layer.H)
	}
}
