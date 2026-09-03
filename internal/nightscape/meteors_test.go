package nightscape

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/meteor"
)

// synthSequence writes n registered frames of a fixed star field, puts one long streak in frame
// `on`, and returns the paths plus the clean stack the frames would sigma-clip to.
//
// The stack is built WITHOUT the streak, which is what a real clip produces: it is the outlier, so it
// never reaches the average. That is also what makes the stack the right thing to subtract.
func synthSequence(t *testing.T, dir string, n, on int) ([]string, *fits.Image) {
	t.Helper()
	const w, h = 900, 700
	rng := rand.New(rand.NewSource(5))

	// One fixed star field, shared by every frame — they are registered, so the stars do not move.
	field := make([]float32, w*h)
	for i := 0; i < 400; i++ {
		cx, cy, amp := rng.Intn(w), rng.Intn(h), 0.05+0.5*rng.Float64()
		for y := cy - 5; y <= cy+5; y++ {
			for x := cx - 5; x <= cx+5; x++ {
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				d := float64((x-cx)*(x-cx) + (y-cy)*(y-cy))
				field[y*w+x] += float32(amp * math.Exp(-d/4))
			}
		}
	}
	clean := fits.NewImage(w, h, 3)
	for c := 0; c < 3; c++ {
		for i := range clean.Pix[c] {
			clean.Pix[c][i] = 0.05 + field[i]
		}
	}

	var paths []string
	for f := 0; f < n; f++ {
		im := fits.NewImage(w, h, 3)
		for c := 0; c < 3; c++ {
			for i := range im.Pix[c] {
				im.Pix[c][i] = 0.05 + field[i] + float32(0.002*rng.NormFloat64())
			}
		}
		if f == on {
			// A long, straight, continuous trail — and a tapered one, so it reads as a meteor rather
			// than as a satellite that the shutter cut off at full brightness.
			for s := 0.0; s <= 420; s += 0.25 {
				x, y := int(200+s*0.9), int(180+s*0.42)
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				taper := math.Sin(math.Pi * s / 420) // dark at both ends, brightest in the middle
				for c := 0; c < 3; c++ {
					im.Pix[c][y*w+x] += float32(0.25 * taper)
				}
			}
		}
		p := filepath.Join(dir, "r_light_"+string(rune('a'+f))+".fits")
		require.NoError(t, im.WriteFITS(p))
		paths = append(paths, p)
	}
	return paths, clean
}

func TestBuildMeteorLayer_FindsTheTrailAndPaintsIt(t *testing.T) {
	dir := t.TempDir()
	paths, clean := synthSequence(t, dir, 6, 3)

	got := buildMeteorLayer(paths, clean, nil, nil, dir)

	require.NotNil(t, got.Layer, "the trail should have been painted")
	assert.Equal(t, 1, got.Painted, "exactly the one trail")

	// It must be painted where it was, and nowhere else.
	mid := 210.0 // halfway along the trail
	mx, my := 200+int(mid*0.9), 180+int(mid*0.42)
	on := got.Layer.Pix[0][my*got.Layer.W+mx]
	assert.Greater(t, float64(on), 0.05, "the trail's middle is empty")
	off := got.Layer.Pix[0][600*got.Layer.W+800]
	assert.Zero(t, off, "the layer must be empty away from the trail, or the blend lifts the whole sky")

	// The stars are in every frame and in the stack, so subtracting the stack must cancel them. A star
	// left in the layer is the failure mode that renders as a speckled envelope around the meteor.
	var painted, starry int
	for i, v := range got.Layer.Pix[0] {
		if v <= 0 {
			continue
		}
		painted++
		if clean.Pix[0][i] > 0.15 { // a star sits here
			starry++
		}
	}
	require.Positive(t, painted)
	assert.Less(t, float64(starry)/float64(painted), 0.05, "stars leaked into the layer")
}

func TestBuildMeteorLayer_RecordsEveryCandidateWithItsReason(t *testing.T) {
	dir := t.TempDir()
	paths, clean := synthSequence(t, dir, 6, 3)

	buildMeteorLayer(paths, clean, nil, nil, dir)

	b, err := os.ReadFile(filepath.Join(dir, meteorJSONFile))
	require.NoError(t, err, "every candidate must be recorded so a dropped meteor can be argued with")
	var got []meteor.Streak
	require.NoError(t, json.Unmarshal(b, &got))
	require.NotEmpty(t, got)
	for _, s := range got {
		assert.NotEmpty(t, s.Class, "every candidate carries a verdict")
		assert.NotEmpty(t, s.Why, "and the reason for it")
	}
}

// A sky with nothing crossing it must produce no layer at all, rather than an empty one that the
// caller would then blend and log.
func TestBuildMeteorLayer_QuietSkyPaintsNothing(t *testing.T) {
	dir := t.TempDir()
	paths, clean := synthSequence(t, dir, 6, -1) // no frame carries a streak

	got := buildMeteorLayer(paths, clean, nil, nil, dir)

	assert.Nil(t, got.Layer)
	assert.Zero(t, got.Painted)
}

// The search must never be able to fail a run: an unreadable frame is a warning and the rest carry on.
func TestBuildMeteorLayer_SoftFailsOnAnUnreadableFrame(t *testing.T) {
	dir := t.TempDir()
	paths, clean := synthSequence(t, dir, 6, 3)
	paths = append(paths, filepath.Join(dir, "does_not_exist.fits"))

	got := buildMeteorLayer(paths, clean, nil, nil, dir)

	assert.NotEmpty(t, got.Warnings, "the missing frame should be reported")
	assert.NotNil(t, got.Layer, "and the trail in the readable frames still found")
}

func TestBuildMeteorLayer_NoFramesOrNoReference(t *testing.T) {
	dir := t.TempDir()
	paths, clean := synthSequence(t, dir, 2, 0)

	assert.Nil(t, buildMeteorLayer(nil, clean, nil, nil, dir).Layer)
	assert.Nil(t, buildMeteorLayer(paths, nil, nil, nil, dir).Layer,
		"without the clean stack there is nothing to measure the frames against")
}

// A streak lying across the FOREGROUND is a coastline, a rooftop or a town's light line, never a
// meteor. Measured on a real low panel the horizon came back 2932 px long — longer than anything that
// ever flew — and was rejected downstream only by a curvature of 6.6 against a limit of 6.0.
func TestKeepInTheSky_DropsWhatLiesAcrossTheGround(t *testing.T) {
	const w, h = 400, 300
	sky := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y < 150 { // the upper half is sky, the lower half is ground
				sky[y*w+x] = 1
			}
		}
	}
	inSky := meteor.Streak{X1: 20, Y1: 30, X2: 380, Y2: 120}
	onGround := meteor.Streak{X1: 20, Y1: 200, X2: 380, Y2: 260}
	// The horizon runs ALONG the boundary, so its midpoint is ambiguous and its average is decisive —
	// which is why the test is the mean and not the middle.
	horizon := meteor.Streak{X1: 10, Y1: 150, X2: 390, Y2: 150}

	got := keepInTheSky([]meteor.Streak{inSky, onGround, horizon}, sky, w, h)

	require.Len(t, got, 1, "only the streak crossing open sky survives")
	assert.Equal(t, inSky.X1, got[0].X1)
}

// With no mask — a frame aimed at the zenith has no ground at all — nothing is guessed at.
func TestKeepInTheSky_KeepsEverythingWithoutAMask(t *testing.T) {
	ss := []meteor.Streak{{X1: 0, Y1: 0, X2: 100, Y2: 50}}
	assert.Len(t, keepInTheSky(ss, nil, 400, 300), 1)
	assert.Len(t, keepInTheSky(ss, make([]float32, 7), 400, 300), 1, "a mismatched mask is not trusted")
}
