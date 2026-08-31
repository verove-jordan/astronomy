package meteor

import (
	"math"
	"math/rand"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// The running extremum is the one piece of this detector that is pure algorithm with a known right
// answer, so it is checked against the definition rather than against itself.
func TestLineBuf_ExtremeMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	b := newLineBuf(64, 64)
	for _, n := range []int{1, 2, 5, 17, 64} {
		for _, k := range []int{1, 3, 7, 21, 65} {
			for _, useMax := range []bool{false, true} {
				vals := make([]float32, n)
				for i := range vals {
					vals[i] = float32(rng.NormFloat64())
				}
				copy(b.vals[:n], vals)
				b.extreme(n, k, useMax)
				got := make([]float32, n)
				copy(got, b.res[:n])
				want := bruteExtreme(vals, k, useMax)
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("n=%d k=%d max=%v at %d: got %v want %v", n, k, useMax, i, got[i], want[i])
					}
				}
			}
		}
	}
}

// bruteExtreme is the definition: the extremum over the centred window, edges replicating.
func bruteExtreme(v []float32, k int, useMax bool) []float32 {
	n := len(v)
	if k%2 == 0 {
		k++
	}
	if k > n {
		k = n
		if k%2 == 0 {
			k--
		}
	}
	r := k / 2
	out := make([]float32, n)
	for i := range v {
		e := v[i]
		for d := -r; d <= r; d++ {
			j := i + d
			if j < 0 {
				j = 0
			} else if j >= n {
				j = n - 1
			}
			if (useMax && v[j] > e) || (!useMax && v[j] < e) {
				e = v[j]
			}
		}
		out[i] = e
	}
	return out
}

// The walks must partition the image. If a pixel were visited twice the erosion would be applied
// twice to it, and if it were missed it would keep its unfiltered value and read as a detection.
func TestLineBuf_WalksVisitEveryPixelExactlyOnce(t *testing.T) {
	const w, h = 37, 23
	b := newLineBuf(w, h)
	for a := 0; a < 32; a++ {
		theta := math.Pi * float64(a) / 32
		seen := make([]int, w*h)
		b.eachWalk(w, h, theta, func(n int) {
			for j := 0; j < n; j++ {
				seen[b.idx[j]]++
			}
		})
		for i, c := range seen {
			if c != 1 {
				t.Fatalf("angle %d (%.1f deg): pixel (%d,%d) visited %d times", a, theta*180/math.Pi, i%w, i/w, c)
			}
		}
	}
}

// Consecutive samples in a walk must be adjacent, or the structuring element is a dotted line and the
// erosion measures gaps rather than the streak.
func TestLineBuf_WalksAreConnected(t *testing.T) {
	const w, h = 41, 29
	b := newLineBuf(w, h)
	for a := 0; a < 32; a++ {
		theta := math.Pi * float64(a) / 32
		b.eachWalk(w, h, theta, func(n int) {
			for j := 1; j < n; j++ {
				p, q := int(b.idx[j-1]), int(b.idx[j])
				dx, dy := q%w-p%w, q/w-p/w
				if dx*dx > 1 || dy*dy > 1 {
					t.Fatalf("angle %.1f deg: step from (%d,%d) to (%d,%d) is not adjacent",
						theta*180/math.Pi, p%w, p/w, q%w, q/w)
				}
			}
		})
	}
}

// This is the claim the whole detector rests on: a line survives the opening and a dot does not,
// whatever their relative brightness. The dot here is made FOUR TIMES brighter than the streak,
// because that is the real case — the stars in these frames are brighter than the meteors.
func TestLinearResponse_KeepsALineAndDeletesABrighterDot(t *testing.T) {
	const w, h = 200, 200
	z := make([]float32, w*h)
	// A bright compact star at (50,50).
	for y := 44; y <= 56; y++ {
		for x := 44; x <= 56; x++ {
			d := float64((x-50)*(x-50) + (y-50)*(y-50))
			z[y*w+x] += float32(40 * math.Exp(-d/8))
		}
	}
	// A faint streak from (120,40) to (180,100): a quarter the star's peak.
	for s := 0.0; s <= 60; s += 0.25 {
		x, y := int(120+s), int(40+s)
		z[y*w+x] = 10
	}
	o := DefaultStreakOptions()
	o.Bin = 1
	r := LinearResponse(z, w, h, o)

	starPeak := 0.0
	for y := 40; y <= 60; y++ {
		for x := 40; x <= 60; x++ {
			starPeak = math.Max(starPeak, float64(r[y*w+x]))
		}
	}
	linePeak := 0.0
	for s := 10.0; s <= 50; s++ {
		x, y := int(120+s), int(40+s)
		linePeak = math.Max(linePeak, float64(r[y*w+x]))
	}
	if linePeak < 5 {
		t.Fatalf("the streak did not survive the opening: peak response %.2f", linePeak)
	}
	if starPeak > 1 {
		t.Fatalf("the star survived the opening: peak response %.2f (streak %.2f)", starPeak, linePeak)
	}
}

// End to end on a synthetic frame: stars everywhere, one streak, and the streak is the only thing
// reported.
func TestDetectStreaks_FindsTheStreakAmongStars(t *testing.T) {
	const w, h = 512, 384
	im := fits.NewImage(w, h, 3)
	rng := rand.New(rand.NewSource(11))
	for c := 0; c < 3; c++ {
		for i := range im.Pix[c] {
			im.Pix[c][i] = float32(0.02 + 0.001*rng.NormFloat64())
		}
	}
	addDot := func(cx, cy int, amp float64) {
		for y := cy - 5; y <= cy+5; y++ {
			for x := cx - 5; x <= cx+5; x++ {
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				d := float64((x-cx)*(x-cx) + (y-cy)*(y-cy))
				v := float32(amp * math.Exp(-d/4))
				for c := 0; c < 3; c++ {
					im.Pix[c][y*w+x] += v
				}
			}
		}
	}
	for i := 0; i < 300; i++ {
		addDot(rng.Intn(w), rng.Intn(h), 0.05+0.4*rng.Float64())
	}
	// The streak is fainter than most of the stars: 0.02 against amplitudes up to 0.45.
	for s := 0.0; s <= 240; s += 0.25 {
		x, y := int(140+s*0.9), int(300-s*0.6)
		if x < 0 || y < 0 || x >= w || y >= h {
			continue
		}
		for c := 0; c < 3; c++ {
			im.Pix[c][y*w+x] += 0.02
		}
	}
	o := DefaultStreakOptions()
	o.Bin = 2
	got := DetectStreaks(im, 3, o)
	if len(got) == 0 {
		t.Fatal("the streak was not found at all")
	}
	// The count is NOT pinned. Three hundred stars scattered at random throw up a few chance
	// alignments long enough to clear the structuring element, and how many is a property of this
	// seed rather than of the detector. What must hold is that the real streak stands clear of them,
	// which is what the caller ranks on.
	best := 0
	for i, s := range got {
		if s.LengthPx > got[best].LengthPx {
			best = i
		}
	}
	s := got[best]
	for i, o := range got {
		if i == best {
			continue
		}
		if o.PeakExcess > s.PeakExcess/4 || o.LengthPx > s.LengthPx/2 {
			t.Errorf("a chance alignment rivals the streak: len %.0f peak %.1f against len %.0f peak %.1f",
				o.LengthPx, o.PeakExcess, s.LengthPx, s.PeakExcess)
		}
	}
	if s.Frame != 3 {
		t.Errorf("frame = %d, want 3", s.Frame)
	}
	// The streak runs at atan2(-0.6, 0.9) which is 146 degrees as an undirected angle.
	if d := angleDiff(s.Angle(), math.Mod(math.Atan2(-0.6, 0.9)+math.Pi, math.Pi)); d > 5*math.Pi/180 {
		t.Errorf("angle off by %.1f degrees", d*180/math.Pi)
	}
	if s.LengthPx < 200 {
		t.Errorf("length %.0f px, want the full ~288 px streak", s.LengthPx)
	}
}

// Coordinates must come back in the frame's own pixels whatever the binning, or a caller compositing
// the streak back would paint it at a quarter scale.
func TestDetectStreaks_ReportsNativePixelsWhateverTheBin(t *testing.T) {
	const w, h = 512, 512
	build := func() *fits.Image {
		im := fits.NewImage(w, h, 1)
		rng := rand.New(rand.NewSource(3))
		for i := range im.Pix[0] {
			im.Pix[0][i] = float32(0.02 + 0.0005*rng.NormFloat64())
		}
		for s := 0.0; s <= 300; s += 0.25 {
			x, y := int(100+s), int(100+s)
			im.Pix[0][y*w+x] += 0.03
		}
		return im
	}
	var lengths []float64
	for _, bin := range []int{2, 4} {
		o := DefaultStreakOptions()
		o.Bin = bin
		got := DetectStreaks(build(), 0, o)
		if len(got) == 0 {
			t.Fatalf("bin %d found nothing", bin)
		}
		lengths = append(lengths, got[0].LengthPx)
		if got[0].X1 < 50 || got[0].X1 > 200 {
			t.Errorf("bin %d: start x %.0f is not near the streak's native start of 100", bin, got[0].X1)
		}
	}
	if math.Abs(lengths[0]-lengths[1]) > 0.15*lengths[0] {
		t.Errorf("length depends on the bin: %.0f at bin 2 vs %.0f at bin 4", lengths[0], lengths[1])
	}
}
