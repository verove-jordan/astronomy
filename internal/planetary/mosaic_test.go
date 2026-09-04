package planetary

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// syntheticSurface builds a large, feature-rich "lunar surface": a smooth illumination ramp plus
// many craters, so cross-correlation has real structure to lock onto at every scale.
func syntheticSurface(w, h int, seed int64) *fits.Image {
	im := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Pix[0][y*w+x] = float32(0.35 + 0.15*math.Sin(float64(x)/220) + 0.1*math.Cos(float64(y)/180))
		}
	}
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < 900; i++ {
		cx, cy := rng.Float64()*float64(w), rng.Float64()*float64(h)
		r := 6 + rng.Float64()*26
		amp := float32(0.12 + rng.Float64()*0.3)
		if rng.Intn(2) == 0 {
			amp = -amp
		}
		for y := int(cy - r); y <= int(cy+r); y++ {
			if y < 0 || y >= h {
				continue
			}
			for x := int(cx - r); x <= int(cx+r); x++ {
				if x < 0 || x >= w {
					continue
				}
				d := math.Hypot(float64(x)-cx, float64(y)-cy)
				if d > r {
					continue
				}
				f := float32(1 - d/r)
				im.Pix[0][y*w+x] += amp * f * f
			}
		}
	}
	return im
}

// cropAt cuts a w×h window whose top-left corner sits at surface position (ox,oy).
func cropAt(src *fits.Image, ox, oy, w, h int) *fits.Image {
	out := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		sy := oy + y
		for x := 0; x < w; x++ {
			sx := ox + x
			if sx < 0 || sy < 0 || sx >= src.W || sy >= src.H {
				continue
			}
			out.Pix[0][y*w+x] = src.Pix[0][sy*src.W+sx]
		}
	}
	return out
}

// TestTrackDriftRecoversASweep: a window walked across a synthetic surface must produce a trajectory
// that is the NEGATED window motion (the subject slides the other way across the field), to within a
// pixel. This is the sign convention the whole canvas placement rests on.
func TestTrackDriftRecoversASweep(t *testing.T) {
	dir := t.TempDir()
	surf := syntheticSurface(1400, 1000, 7)
	const fw, fh, n = 500, 400, 14
	stepX, stepY := 40, 25
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		im := cropAt(surf, 100+i*stepX, 80+i*stepY, fw, fh)
		p := filepath.Join(dir, fmt.Sprintf("f_%03d.fits", i))
		if err := im.WriteFITS(p); err != nil {
			t.Fatalf("write frame: %v", err)
		}
		paths[i] = p
	}
	drift, w, h, err := trackDrift(context.Background(), paths, nil)
	if err != nil {
		t.Fatalf("trackDrift: %v", err)
	}
	if w != fw || h != fh {
		t.Fatalf("frame dims = %dx%d, want %dx%d", w, h, fw, fh)
	}
	for i := 0; i < n; i++ {
		wantX, wantY := float64(-i*stepX), float64(-i*stepY)
		if math.Abs(drift[i].X-wantX) > 2 || math.Abs(drift[i].Y-wantY) > 2 {
			t.Errorf("frame %d drift = (%.1f,%.1f), want (%.1f,%.1f)", i, drift[i].X, drift[i].Y, wantX, wantY)
		}
	}
}

// TestSegmentDriftSplitsOnlyWhenItMust pins the gate: a trajectory that stays inside one field is a
// single panel, and a long sweep is cut into overlapping panels that between them cover every frame.
func TestSegmentDriftSplitsOnlyWhenItMust(t *testing.T) {
	const w, h = 600, 400
	tight := make([]driftPoint, 40)
	for i := range tight {
		tight[i] = driftPoint{X: float64(i) * 0.5, Y: 0} // 20 px total on a 400 px axis
	}
	if got := driftSpan(tight); got > float64(h)*driftSinglePanelFrac {
		t.Fatalf("tight span %.0f should be inside the single-panel gate", got)
	}

	long := make([]driftPoint, 300)
	for i := range long {
		long[i] = driftPoint{X: float64(i) * 12, Y: float64(i) * 3}
	}
	panels := segmentDrift(long, w, h)
	if len(panels) < 3 {
		t.Fatalf("a %.0f px sweep on a %d px axis gave only %d panels", driftSpan(long), h, len(panels))
	}
	// Every frame must land in at least one panel, and each panel must stay within its drift budget.
	covered := make([]bool, len(long))
	for _, p := range panels {
		ax, ay := long[p.Idx[0]].X, long[p.Idx[0]].Y
		for _, i := range p.Idx {
			covered[i] = true
			if d := math.Hypot(long[i].X-ax, long[i].Y-ay); d > float64(h)*panelDriftFrac+1e-6 {
				t.Errorf("panel spans %.0f px, over the %.0f px budget", d, float64(h)*panelDriftFrac)
			}
		}
		if len(p.Idx) < minPanelFrames {
			t.Errorf("panel has %d frames, under the %d minimum", len(p.Idx), minPanelFrames)
		}
	}
	for i, c := range covered {
		if !c {
			t.Fatalf("frame %d is in no panel", i)
		}
	}
	// Consecutive panels must overlap, or the canvas has nothing to blend across.
	for i := 1; i < len(panels); i++ {
		prevLast := panels[i-1].Idx[len(panels[i-1].Idx)-1]
		if panels[i].Idx[0] > prevLast {
			t.Errorf("panels %d and %d do not overlap (%d then %d)", i-1, i, prevLast, panels[i].Idx[0])
		}
	}
}

// TestAssemblePanelsReconstructsTheSurface is the end-to-end geometry proof: panels cut from known
// positions on a surface, handed seeds carrying a deliberate error, must be refined back into place
// and blended into a canvas that reproduces the original surface.
func TestAssemblePanelsReconstructsTheSurface(t *testing.T) {
	dir := t.TempDir()
	surf := syntheticSurface(1200, 700, 11)
	const pw, ph = 480, 420
	type place struct{ x, y int }
	places := []place{{80, 120}, {380, 150}, {660, 130}}
	masters := make([]string, len(places))
	labels := make([]string, len(places))
	seeds := make([]struct{ X, Y float64 }, len(places))
	for i, pl := range places {
		im := cropAt(surf, pl.x, pl.y, pw, ph)
		p := filepath.Join(dir, fmt.Sprintf("m_%d.fits", i))
		if err := im.WriteFITS(p); err != nil {
			t.Fatalf("write master: %v", err)
		}
		masters[i] = p
		labels[i] = fmt.Sprintf("p%02d", i+1)
		// Seed with a deliberate error the refinement has to remove.
		seeds[i] = struct{ X, Y float64 }{X: float64(pl.x-places[0].x) + 9, Y: float64(pl.y-places[0].y) - 7}
	}
	canvas, notes, err := assemblePanels(context.Background(), masters, labels, seeds)
	if err != nil {
		t.Fatalf("assemblePanels: %v", err)
	}
	for _, n := range notes {
		t.Log(n)
	}
	// The refinement must actually RUN, not fall back. This assertion exists because it once did
	// fall back — silently, on every panel — and the pixel comparison below still passed, since a
	// feathered blend of slightly-misplaced panels is smooth enough to slip under a tolerance. A
	// fallback note is the only direct evidence, so it is checked directly.
	for _, n := range notes {
		if strings.Contains(n, "drift track") || strings.Contains(n, "weak overlap") {
			t.Errorf("panel placement fell back instead of refining: %s", n)
		}
	}
	if canvas.W <= pw {
		t.Fatalf("canvas width %d is no bigger than one panel (%d) — the panels were stacked on top of each other", canvas.W, pw)
	}
	// The canvas origin is the leftmost/topmost panel corner, which is panel 0 at surface
	// (places[0].x, places[0].y). Compare the interior against the surface it was cut from.
	var worst float64
	var count int
	for y := 30; y < canvas.H-30; y += 3 {
		for x := 30; x < canvas.W-30; x += 3 {
			want := surf.Pix[0][(y+places[0].y)*surf.W+(x+places[0].x)]
			got := canvas.Pix[0][y*canvas.W+x]
			if got == 0 {
				continue // uncovered corner of the union bbox
			}
			if d := math.Abs(float64(got - want)); d > worst {
				worst = d
			}
			count++
		}
	}
	if count < 1000 {
		t.Fatalf("only %d canvas pixels compared — the canvas is mostly empty", count)
	}
	if worst > 0.02 {
		t.Errorf("worst canvas deviation %.4f from the source surface (want ≤0.02) — panels are misplaced", worst)
	}
}

// TestVideoFrameStep pins the extraction budget: a clip inside the budget is untouched (step 1, the
// historical "extract everything"), and one over it is decimated to at most the budget, evenly.
func TestVideoFrameStep(t *testing.T) {
	for _, tc := range []struct {
		total, budget, wantStep, wantKeptMax int
	}{
		{0, 1000, 1, 0},         // unknown frame count → extract everything
		{800, 1000, 1, 800},     // inside the budget → untouched
		{1000, 1000, 1, 1000},   // exactly the budget → untouched
		{33147, 1445, 23, 1445}, // a 4K120 phone sweep → decimated
	} {
		step, kept := videoFrameStep(tc.total, tc.budget)
		if step != tc.wantStep {
			t.Errorf("videoFrameStep(%d,%d) step = %d, want %d", tc.total, tc.budget, step, tc.wantStep)
		}
		if kept > tc.wantKeptMax {
			t.Errorf("videoFrameStep(%d,%d) kept %d frames, over the %d budget", tc.total, tc.budget, kept, tc.wantKeptMax)
		}
	}
}

// TestVideoFrameBudgetScalesWithFrameArea: the budget is a pixel budget, so a small planetary ROI
// keeps far more frames than a 4K clip — and both stay inside the clamps.
func TestVideoFrameBudgetScalesWithFrameArea(t *testing.T) {
	uhd := videoFrameBudget(3840, 2160, 0)
	roi := videoFrameBudget(640, 480, 0)
	if uhd >= roi {
		t.Errorf("4K budget %d should be smaller than a 640x480 budget %d", uhd, roi)
	}
	if uhd < minVideoFrames || roi > maxVideoFrames {
		t.Errorf("budgets out of clamps: 4K=%d roi=%d (min %d max %d)", uhd, roi, minVideoFrames, maxVideoFrames)
	}
	if got := videoFrameBudget(0, 0, 0); got != minVideoFrames {
		t.Errorf("unknown frame size budget = %d, want the floor %d", got, minVideoFrames)
	}
}

// TestVideoFrameBudgetRespectsFreeDisk: the budget must shrink to fit the scratch actually
// available. A fixed budget assumes a machine with room, and guessing wrong does not produce a poor
// stack — it fills the filesystem.
func TestVideoFrameBudgetRespectsFreeDisk(t *testing.T) {
	const w, h = 3840, 2160
	roomy := videoFrameBudget(w, h, 500<<30) // 500 GB free
	tight := videoFrameBudget(w, h, 8<<30)   // 8 GB free
	if tight >= roomy {
		t.Errorf("budget on a tight disk (%d) should be below a roomy one (%d)", tight, roomy)
	}
	// The chosen count must actually fit in the share of the disk it may claim.
	perFrame := float64(w) * float64(h) * 3 * 2 * fitsOverhead
	if want := int(float64(8<<30) * scratchShare / perFrame); tight > want && tight > minVideoFrames {
		t.Errorf("budget %d exceeds what 8 GB allows (%d)", tight, want)
	}
}

// TestNormalizePedestal: a master whose sky sits far above zero (a very short exposure, where the
// bias pedestal dwarfs the signal) must have that pedestal removed, while an ordinary master with a
// dark sky is left on the historical pure scale.
func TestNormalizePedestal(t *testing.T) {
	// A disc on a high pedestal: sky 0.80, disc peaks at 0.95. The frame is master-sized on purpose —
	// fitLunarDisc works on a downsample, so a toy 400x300 image has no limb left to fit.
	withPedestal := discImage(1200, 900, 0.80, 0.95)
	normalize(withPedestal, stackNormPct)
	sky := withPedestal.Pix[0][2] // a corner pixel, well off the disc
	if sky > 0.15 {
		t.Errorf("pedestal not removed: sky normalized to %.3f, want near 0", sky)
	}

	// A dark-sky master must be scaled exactly as before (pure 1/hi, sky stays near 0 anyway).
	dark := discImage(1200, 900, 0.01, 0.90)
	before := dark.Clone()
	normalize(dark, stackNormPct)
	hi := planePercentile(before.Pix[0], stackNormPct)
	for i := range dark.Pix[0] {
		want := before.Pix[0][i] * float32(1/hi)
		if math.Abs(float64(dark.Pix[0][i]-want)) > 1e-5 {
			t.Fatalf("dark-sky master changed at %d: got %.6f want %.6f (historical scale must be preserved)", i, dark.Pix[0][i], want)
		}
	}
}

// discImage paints a filled disc of value peak on a uniform background of value sky.
func discImage(w, h int, sky, peak float32) *fits.Image {
	im := fits.NewImage(w, h, 1)
	cx, cy, r := float64(w)/2, float64(h)/2, float64(h)/3.6
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := sky
			if math.Hypot(float64(x)-cx, float64(y)-cy) <= r {
				v = peak
			}
			im.Pix[0][y*w+x] = v
		}
	}
	return im
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }

// TestTrackDriftRecoversADiscreteJump: a re-pointed burst series moves the subject by a large
// fraction of the frame between bursts — far more than any seeded search covers. The tracker must
// recover that jump, because a missed one is inherited by every later frame and silently merges two
// different pointings into one panel.
func TestTrackDriftRecoversADiscreteJump(t *testing.T) {
	dir := t.TempDir()
	surf := syntheticSurface(2600, 2000, 3)
	const fw, fh = 900, 700
	// Two bursts: a slow drift within each, and a big re-point between them.
	type win struct{ x, y int }
	var wins []win
	for i := 0; i < 8; i++ {
		wins = append(wins, win{200 + i*12, 150 + i*5})
	}
	// A jump of (320,240): well past the wide search (0.25x700 = 175 px), inside the recovery's
	// +/-min(W,H)/2 reach, and still leaving the two bursts a real overlap to correlate over — which
	// is the case that actually occurs, since a re-point that leaves NO overlap cannot be recovered
	// by any correlation and is not something the tracker can be asked to do.
	for i := 0; i < 8; i++ {
		wins = append(wins, win{520 + i*12, 390 + i*5})
	}
	paths := make([]string, len(wins))
	for i, w := range wins {
		im := cropAt(surf, w.x, w.y, fw, fh)
		p := filepath.Join(dir, fmt.Sprintf("f_%03d.fits", i))
		if err := im.WriteFITS(p); err != nil {
			t.Fatalf("write frame: %v", err)
		}
		paths[i] = p
	}
	drift, _, _, err := trackDrift(context.Background(), paths, nil)
	if err != nil {
		t.Fatalf("trackDrift: %v", err)
	}
	// The trajectory is the negated window motion; the jump must show up at the burst boundary.
	//
	// Tolerance is 5% of the frame, not sub-pixel, and deliberately so: this trajectory exists to
	// GROUP frames into panels that share a field. Where each panel actually lands on the canvas is
	// re-measured at full resolution by refineAgainst — TestAssemblePanelsReconstructsTheSurface
	// starts from seeds that are wrong by 9 px and still reproduces the surface exactly. What would
	// be a real failure here is missing the jump altogether (reading a re-point as "barely moved"),
	// which is an error of the size of the jump, not of a few percent.
	tol := 0.05 * float64(min(fw, fh))
	for i, w := range wins {
		wantX := float64(-(w.x - wins[0].x))
		wantY := float64(-(w.y - wins[0].y))
		if math.Abs(drift[i].X-wantX) > tol || math.Abs(drift[i].Y-wantY) > tol {
			t.Fatalf("frame %d drift = (%.0f,%.0f), want (%.0f,%.0f) — the re-point was not recovered",
				i, drift[i].X, drift[i].Y, wantX, wantY)
		}
	}
	// And the two bursts must land in different panels, not be averaged into one.
	panels := segmentDrift(drift, fw, fh)
	if len(panels) < 2 {
		t.Fatalf("two pointings 400 px apart gave %d panel(s)", len(panels))
	}
}

// TestTrackDriftAnchorsOnTheLimb reproduces the geometry of a real re-pointed lunar burst series:
// a Moon LARGER than the frame (disc radius 2670 px on a 5202x3464 sensor), shot at three pointings
// with a slow drift inside each burst and a ~2,280 px jump between them.
//
// That jump is the case pure correlation cannot survive. The two pointings share only about a third
// of their height, and a step measured wrongly is inherited by every later frame: the run that
// prompted this test reported 59,354 px of drift across a 3,464 px frame — 1713% — and segmented 60
// frames into 34 panels. The limb is an absolute reference and every such frame has one, so the
// trajectory must come out right regardless of what correlation alone would have done.
func TestTrackDriftAnchorsOnTheLimb(t *testing.T) {
	dir := t.TempDir()
	const fw, fh = 1300, 866 // the real 5202x3464 at 1/4, to keep the test quick
	const discR = 667.5      // 2670/4
	// Disc centre in FRAME coordinates, per burst, quartered from the measured values.
	centres := [][2]float64{}
	for _, b := range [][2]float64{{4087, 2835}, {3181, 583}, {3208, 1600}} {
		for i := 0; i < 8; i++ {
			centres = append(centres, [2]float64{(b[0] + float64(i)*30) / 4, (b[1] + float64(i)*4) / 4})
		}
	}
	paths := make([]string, len(centres))
	for i, c := range centres {
		paths[i] = filepath.Join(dir, fmt.Sprintf("f_%03d.fits", i))
		if err := moonFrame(fw, fh, c[0], c[1], discR, int64(i)).WriteFITS(paths[i]); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}
	drift, _, _, err := trackDrift(context.Background(), paths, nil)
	if err != nil {
		t.Fatalf("trackDrift: %v", err)
	}
	// The trajectory tracks where the subject sits in the field: exactly the disc-centre motion.
	tol := 0.04 * float64(min(fw, fh))
	for i, c := range centres {
		wantX, wantY := c[0]-centres[0][0], c[1]-centres[0][1]
		if math.Abs(drift[i].X-wantX) > tol || math.Abs(drift[i].Y-wantY) > tol {
			t.Errorf("frame %d drift = (%.0f,%.0f), want (%.0f,%.0f)", i, drift[i].X, drift[i].Y, wantX, wantY)
		}
	}
	if span := driftSpan(drift); span > 3*float64(fh) {
		t.Errorf("drift span %.0f px on a %d px frame is not physical", span, fh)
	}
	// Three pointings must give a handful of panels, not one per frame.
	panels := segmentDrift(drift, fw, fh)
	if len(panels) < 2 || len(panels) > 8 {
		t.Errorf("three pointings segmented into %d panels, want a handful", len(panels))
	}
}

// moonFrame paints a cratered lunar disc of radius r centred at (cx,cy), which may lie outside the
// frame — the Moon overflowing the sensor is the whole point of the case under test.
func moonFrame(w, h int, cx, cy, r float64, seed int64) *fits.Image {
	im := fits.NewImage(w, h, 1)
	rng := rand.New(rand.NewSource(99)) // one fixed surface, so features are consistent across frames
	type crater struct {
		x, y, rad float64
		amp       float32
	}
	var craters []crater
	for i := 0; i < 400; i++ {
		a, d := rng.Float64()*2*math.Pi, math.Sqrt(rng.Float64())*r
		craters = append(craters, crater{math.Cos(a) * d, math.Sin(a) * d, 8 + rng.Float64()*30,
			float32(0.1 + rng.Float64()*0.25)})
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if math.Hypot(dx, dy) > r {
				continue // sky stays 0
			}
			v := float32(0.55)
			for _, c := range craters {
				if d := math.Hypot(dx-c.x, dy-c.y); d < c.rad {
					f := float32(1 - d/c.rad)
					v += c.amp * f * f
				}
			}
			im.Pix[0][y*w+x] = v
		}
	}
	return im
}

// TestNeutralizeMaster: a debayered raw arrives green (twice as many green photosites as red or
// blue and no white balance applied). Equalizing against the LIT surface must neutralize it — and a
// master that is already neutral must be left alone.
func TestNeutralizeMaster(t *testing.T) {
	tests := []struct {
		name       string
		gainR      float32
		gainB      float32
		wantChange bool
	}{
		{"green cast from an unbalanced CFA raw", 0.54, 0.59, true},
		{"already neutral", 1.0, 1.0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := fits.NewImage(400, 300, 3)
			cx, cy, r := 200.0, 150.0, 90.0
			for y := 0; y < 300; y++ {
				for x := 0; x < 400; x++ {
					i := y*400 + x
					var v float32
					if math.Hypot(float64(x)-cx, float64(y)-cy) <= r {
						v = 0.6
					}
					im.Pix[0][i] = v * tt.gainR
					im.Pix[1][i] = v
					im.Pix[2][i] = v * tt.gainB
				}
			}
			p := filepath.Join(t.TempDir(), "m.fits")
			if err := im.WriteFITS(p); err != nil {
				t.Fatalf("write: %v", err)
			}
			note, err := neutralizeMaster(p)
			if err != nil {
				t.Fatalf("neutralizeMaster: %v", err)
			}
			if tt.wantChange != (note != "") {
				t.Fatalf("note = %q, wantChange=%v", note, tt.wantChange)
			}
			out, err := fits.ReadImage(p)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			// After balancing, the lit disc must be neutral in all three channels.
			ci := 150*400 + 200
			r0, g0, b0 := out.Pix[0][ci], out.Pix[1][ci], out.Pix[2][ci]
			if g0 == 0 {
				t.Fatal("lit centre is black")
			}
			for _, ratio := range []float64{float64(r0 / g0), float64(b0 / g0)} {
				if math.Abs(ratio-1) > 0.02 {
					t.Errorf("lit surface not neutral: R/G=%.3f B/G=%.3f", r0/g0, b0/g0)
				}
			}
		})
	}
}

// TestFirstCSVField pins the parse against ffprobe's ACTUAL output. `-of csv=p=0` still writes the
// field separator, so a one-value query returns "33147," — and an unparsed frame count silently
// disables the extraction budget, which is how a 4-minute 4K clip filled a disk with 12,000 frames.
func TestFirstCSVField(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"trailing separator (what ffprobe really emits)", "33147,\n", "33147"},
		{"bare value", "33147\n", "33147"},
		{"multi-field", "3840,2160,\n", "3840"},
		{"empty", "\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstCSVField(tt.in); got != tt.want {
				t.Errorf("firstCSVField(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSeededAlignmentBeatsCentroidOnAClippedDisc pins the fix for the defect that produced a second
// Moon in every hand-guided lunar stack.
//
// The aligner seeds each frame from the brightness centroid of the whole frame. That is only a
// position reference while the entire body is inside the frame; once the Moon overflows the sensor —
// the normal case at high magnification — the centroid of the VISIBLE light moves with the framing,
// so the seed is wrong by however far the frame moved and the ±64 px coarse stage cannot recover it.
// Frames then stack at their own offsets and the master contains several displaced copies.
//
// Measured on the real iPhone stills: limb width 40.0 px centroid-seeded vs 29.0 px trajectory-seeded
// against a 23.5 px single frame, i.e. 1.70x smearing reduced to 1.23x.
func TestSeededAlignmentBeatsCentroidOnAClippedDisc(t *testing.T) {
	dir := t.TempDir()
	// A disc far larger than the frame, so it is clipped on every side and the visible centroid is
	// governed by the crop rather than by the surface.
	const fw, fh = 700, 700
	const discR = 900.0
	centres := [][2]float64{}
	for i := 0; i < 10; i++ {
		centres = append(centres, [2]float64{350 + float64(i)*22, 350 + float64(i)*14}) // ~26 px/frame
	}
	paths := make([]string, len(centres))
	scores := make([]float64, len(centres))
	for i, c := range centres {
		paths[i] = filepath.Join(dir, fmt.Sprintf("f_%03d.fits", i))
		if err := moonFrame(fw, fh, c[0], c[1], discR, 0).WriteFITS(paths[i]); err != nil {
			t.Fatalf("write: %v", err)
		}
		scores[i] = 1 // equal sharpness: only alignment can differ
	}
	// The true trajectory: the subject sits where the disc centre sits.
	traj := make([]driftPoint, len(centres))
	for i, c := range centres {
		traj[i] = driftPoint{X: c[0] - centres[0][0], Y: c[1] - centres[0][1]}
	}

	alignWith := func(name string, seeds []frameSeed) int {
		out := filepath.Join(dir, name)
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		res, err := warpToSharpest(context.Background(), paths, scores, out, "a", true, 1, 0, seeds, nil)
		if err != nil {
			t.Fatalf("%s: warp: %v", name, err)
		}
		return len(res.paths)
	}

	centroid := alignWith("centroid", nil)
	seeded := alignWith("seeded", seedsFromTrajectory(traj, scores))
	t.Logf("frames aligned: centroid-seeded %d/%d   trajectory-seeded %d/%d",
		centroid, len(paths), seeded, len(paths))

	// What the seed buys is USABLE FRAMES. The correlation gate already stops a mislocked frame from
	// reaching the master, so a bad seed does not show up as a ghost — it shows up as most of the
	// capture being thrown away. On a real video panel the centroid seed aligned 30 of 40 frames and
	// the trajectory seed aligned all 40.
	if seeded <= centroid {
		t.Errorf("trajectory seeding aligned %d frames, no better than the centroid's %d", seeded, centroid)
	}
	if seeded < len(paths) {
		t.Errorf("trajectory seeding aligned only %d of %d frames — the prior should make every frame usable",
			seeded, len(paths))
	}
}

// TestRotationIsMeasuredAndCancelled pins the third failure mode behind a ghosted lunar stack.
//
// The aligner models a frame as a translation plus small per-point corrections, and an alignment
// point searches only ±apMaxShift (6 px) around its baseline. A rotation displaces a point by θ·r,
// so on a 3840 px frame half a degree moves the edges 17 px — beyond every AP's reach. The frame is
// then warped by a field that fits only its centre and the master gets a rotated second Moon.
// Hand-held afocal frames rotate; this must be measured and cancelled, not absorbed.
func TestRotationIsMeasuredAndCancelled(t *testing.T) {
	tests := []struct {
		name   string
		degree float64
	}{
		{"no rotation", 0},
		{"a third of a degree", 0.33},
		{"a degree and a half, the other way", -1.5},
	}
	base := syntheticSurface(900, 900, 5)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rotated := rotateImage(base, tt.degree*math.Pi/180)
			rc := newRefContext(base)
			tgtBlur := blurPlane(rotated, warpBlur)
			small := downPlane(tgtBlur, coarseDown)
			theta := estimateRotation(rc.small, small,
				comet.Point{X: rc.gWin.X / coarseDown, Y: rc.gWin.Y / coarseDown}, rc.gRadius/coarseDown, 0, 0)
			t.Logf("true %.2f°  measured %.2f°", tt.degree, theta*180/math.Pi)

			// The contract is not the sign of the angle, it is that warping the frame by the field
			// built from it lands back on the reference. estimateRotation and similarityField share
			// one convention; asserting the round trip tests that, and cannot be satisfied by a
			// convention that merely looks right.
			dx, dy := similarityField(theta, float64(base.W)/2, float64(base.H)/2, 0, 0, rc.cx, rc.cy)
			fixed := warpByGrid(rotated, dx, dy)
			before := frameDiff(base, rotated)
			after := frameDiff(base, fixed)
			t.Logf("mean |difference| vs reference: rotated %.5f → corrected %.5f", before, after)
			if tt.degree == 0 {
				if theta != 0 {
					t.Errorf("a non-rotated frame measured %.3f°, want exactly 0", theta*180/math.Pi)
				}
				return
			}
			if after > before*0.6 {
				t.Errorf("correcting the rotation barely helped: %.5f → %.5f", before, after)
			}
		})
	}
}

// frameDiff is the mean absolute difference over the interior (edges are cut by the warp).
func frameDiff(a, b *fits.Image) float64 {
	m := 80
	var sum float64
	var n int
	for y := m; y < a.H-m; y++ {
		for x := m; x < a.W-m; x++ {
			sum += math.Abs(float64(a.Pix[0][y*a.W+x] - b.Pix[0][y*a.W+x]))
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// TestSimilarityFieldReducesToTranslation: at zero rotation the field must be exactly the uniform
// translation grid, so a capture with no measurable rotation warps bit-identically to before.
func TestSimilarityFieldReducesToTranslation(t *testing.T) {
	apx := []float64{10, 500, 10, 500}
	apy := []float64{10, 10, 500, 500}
	dx, dy := similarityField(0, 250, 250, -7.5, 3.25, apx, apy)
	for k := range dx {
		if dx[k] != -7.5 || dy[k] != 3.25 {
			t.Fatalf("node %d = (%.4f,%.4f), want the uniform (-7.5, 3.25)", k, dx[k], dy[k])
		}
	}
}

// rotateImage rotates about the image centre (bilinear; outside → 0).
func rotateImage(im *fits.Image, theta float64) *fits.Image {
	out := fits.NewImage(im.W, im.H, 1)
	cx, cy := float64(im.W)/2, float64(im.H)/2
	sin, cos := math.Sin(theta), math.Cos(theta)
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			ux, uy := float64(x)-cx, float64(y)-cy
			out.Pix[0][y*im.W+x] = bilinear(im, cx+ux*cos-uy*sin, cy+ux*sin+uy*cos)
		}
	}
	return out
}
