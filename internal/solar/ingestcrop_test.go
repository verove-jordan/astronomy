package solar

import (
	"math"
	"testing"
)

// ingestcrop_test.go pins the per-frame crop's coordinate arithmetic.
//
// It converts a limb measured on the SCAN raster into an offset inside a buffer that ffmpeg has
// already cropped to the clip-wide union — a scale and a translation, in that order, with three
// chances to drop one. None of them announce themselves: a wrong offset simply writes a frame with
// the disc off-centre, which the registration then corrects by shifting it back, so the stack still
// comes out looking right while every frame quietly loses the margin on one side.

func TestCentreOnDisc_PutsTheDiscInTheMiddle(t *testing.T) {
	// A decoded union buffer far larger than the disc, which is the case this exists for.
	const uw, uh = 900, 700
	crop := cropRect{x: 120, y: 64, w: uw, h: uh}
	const scale = 2.25 // full-resolution pixels per scan pixel
	const side = 200

	// The disc's true position inside the buffer, and the scan-raster limb that describes it.
	for _, want := range []struct{ bx, by float64 }{
		{450, 350}, // middle
		{80, 90},   // hard against the top-left, where the clamp has to engage
		{860, 640}, // and the bottom-right
	} {
		l := Limb{CX: (want.bx + float64(crop.x)) / scale, CY: (want.by + float64(crop.y)) / scale, R: 70}
		plane := make([]float32, uw*uh)
		// A single bright pixel at the disc centre is enough to find where it landed.
		plane[int(want.by)*uw+int(want.bx)] = 1

		w, h, out := centreOnDisc(plane, crop, scale, side, l)
		if w != side || h != side {
			t.Fatalf("crop came out %dx%d, want %dx%d", w, h, side, side)
		}
		// Find the marker in the output.
		found := -1
		for i, v := range out {
			if v == 1 {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("disc centre at (%.0f,%.0f) fell outside the crop entirely", want.bx, want.by)
		}
		fx, fy := found%side, found/side
		// Away from the edges the disc must land dead centre; against them the clamp shifts it, but
		// it must still be inside.
		clamped := want.bx < side/2 || want.by < side/2 ||
			want.bx > float64(uw-side/2) || want.by > float64(uh-side/2)
		if !clamped {
			if math.Abs(float64(fx)-side/2) > 1 || math.Abs(float64(fy)-side/2) > 1 {
				t.Errorf("disc at (%.0f,%.0f) landed at (%d,%d), want the centre (%d,%d)",
					want.bx, want.by, fx, fy, side/2, side/2)
			}
			continue
		}
		t.Logf("clamped case (%.0f,%.0f) landed at (%d,%d)", want.bx, want.by, fx, fy)
	}
}

// TestCentreOnDisc_KeepsTheWholeBufferWhenItCannotHelp checks the ways out.
func TestCentreOnDisc_KeepsTheWholeBufferWhenItCannotHelp(t *testing.T) {
	crop := cropRect{x: 0, y: 0, w: 64, h: 64}
	plane := make([]float32, 64*64)
	for _, c := range []struct {
		name string
		side int
		limb Limb
	}{
		{"no geometry to centre on", 32, Limb{}},
		{"the crop is not smaller than the buffer", 64, Limb{CX: 32, CY: 32, R: 10}},
		{"no side asked for", 0, Limb{CX: 32, CY: 32, R: 10}},
	} {
		w, h, out := centreOnDisc(plane, crop, 1, c.side, c.limb)
		if w != crop.w || h != crop.h || len(out) != len(plane) {
			t.Errorf("%s: got %dx%d (%d px), want the buffer untouched", c.name, w, h, len(out))
		}
	}
}

// TestDiscCropSide_MatchesTheStillsPath keeps the two ingest paths cutting the same square, so a
// session of clips and a session of stills reach the stack on the same raster.
func TestDiscCropSide_MatchesTheStillsPath(t *testing.T) {
	scan := scanResult{limb: Limb{R: 100}, scale: 2}
	opts := IngestOptions{CropMargin: 0.18}
	if got, want := discCropSide(scan, opts), cropSideFor(200, 0.18); got != want {
		t.Errorf("video crop side %d, stills path %d", got, want)
	}
	// The group's radius wins when triage measured one, so every clip in a group agrees.
	opts.TargetRadius = 300
	if got, want := discCropSide(scan, opts), cropSideFor(300, 0.18); got != want {
		t.Errorf("with a group radius: %d, want %d", got, want)
	}
}
