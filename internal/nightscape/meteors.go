package nightscape

// meteors.go puts back the one thing the clean stack is designed to throw away.
//
// The sigma-clip that builds the sky is right about a meteor and wrong about what to do with it: it
// is bright, it is in a single frame, and it is nothing like what that pixel does in the other thirty,
// so the clip correctly identifies it as an outlier and correctly keeps it out of the average. What it
// should not do is discard it. This finds the streaks in the registered frames, decides which are
// worth keeping, and hands back a layer that can be added to the linear sky before it is graded — so
// the meteor is stretched and coloured with everything else rather than pasted on afterwards.
//
// Detection runs on the REGISTERED frames, which is what makes the layer directly compositable: a
// streak's coordinates already mean the same thing as the stack's, and nothing has to be transformed.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/meteor"
)

const (
	meteorLayerFile = "meteor_layer.fits"
	meteorJSONFile  = "meteors.json"
)

// meteorResult is what buildMeteorLayer found.
type meteorResult struct {
	Layer    *fits.Image
	Painted  int
	Counts   map[meteor.Class]int
	Warnings []string
}

// buildMeteorLayer detects the streaks across the registered frames and paints the confident meteors
// into a layer the size of those frames.
//
// ref is the clean stack, and it is what each frame is measured against: subtracting it cancels the
// stars exactly — they are in both — while cancelling nothing of the meteor, because the clip had
// already removed the meteor from the stack. A smooth sky model instead of the stack leaves every star
// under the trail in the layer, which renders as a speckled envelope around it.
//
// Every error here is soft. A run must never fail because the meteor search did not work out.
func buildMeteorLayer(aligned []string, ref *fits.Image, sky []float32, prep func(*fits.Image), outDir string) meteorResult {
	var out meteorResult
	if len(aligned) == 0 || ref == nil {
		return out
	}
	load := func(i int) (*fits.Image, error) {
		im, err := fits.ReadImage(aligned[i])
		if err != nil {
			return nil, err
		}
		if prep != nil {
			prep(im) // the frames must be in the same light space as the stack, or the subtraction is meaningless
		}
		return im, nil
	}

	so := meteor.DefaultStreakOptions()
	var all []meteor.Streak
	for i := range aligned {
		im, err := load(i)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("meteor search skipped %s: %v", filepath.Base(aligned[i]), err))
			continue
		}
		all = append(all, meteor.DetectStreaks(im, i, so)...)
	}
	if len(all) == 0 {
		return out
	}
	// A meteor is in the SKY. Anything lying across the ground is a coastline, a rooftop or a town's
	// light line, and those are the strongest linear structures in a frame that contains any landscape
	// at all — measured on the low panel of a real session, the horizon came back as a 2932-pixel
	// candidate, longer than anything that ever flew. It was rejected downstream, but only just: its
	// curvature read 6.6 against a straightness limit of 6.0. Filtering by where the line IS removes
	// the whole class at the source instead of leaving it to a threshold that close.
	if before := len(all); before > 0 {
		all = keepInTheSky(all, sky, ref.W, ref.H)
		if dropped := before - len(all); dropped > 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"%d streak(s) ignored for lying across the foreground rather than the sky", dropped))
		}
	}
	if len(all) == 0 {
		return out
	}
	co := meteor.DefaultOptions()
	meteor.Classify(all, co)
	out.Counts = meteor.Counts(all)

	// Everything found is recorded, including what was dropped and why, so a rejected meteor can be
	// argued with. Only the confident ones are painted.
	if b, err := json.MarshalIndent(all, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(outDir, meteorJSONFile), b, 0o644); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("could not write %s: %v", meteorJSONFile, err))
		}
	}
	keep := meteor.Confident(meteor.Kept(all), co)
	if len(keep) == 0 {
		return out
	}
	layer, err := meteor.RenderLayer(load, ref, keep, meteor.DefaultRenderOptions())
	if err != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("meteor layer not rendered: %v", err))
		return out
	}
	out.Layer, out.Painted = layer, len(keep)
	return out
}

// keepInTheSky drops the streaks that do not lie wholly in the sky.
//
// The test is the MEAN sky opacity along the line, not at its midpoint, and the difference matters
// for the one case that most needs catching: the horizon itself runs exactly along the boundary, so
// its midpoint is ambiguous while its average is decisively half. A meteor crossing open sky averages
// one; a rooftop or a light line averages zero.
//
// A nil or mismatched mask means the sky/ground split is unknown — for a frame aimed at the zenith
// there is no ground at all — and everything is kept rather than guessed at.
func keepInTheSky(ss []meteor.Streak, sky []float32, w, h int) []meteor.Streak {
	if len(sky) != w*h {
		return ss
	}
	const minMeanSky = 0.9
	out := ss[:0:0]
	for _, s := range ss {
		n := int(math.Hypot(s.X2-s.X1, s.Y2-s.Y1))
		if n < 1 {
			continue
		}
		var sum float64
		for i := 0; i <= n; i++ {
			t := float64(i) / float64(n)
			x := int(math.Round(s.X1 + t*(s.X2-s.X1)))
			y := int(math.Round(s.Y1 + t*(s.Y2-s.Y1)))
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			sum += float64(sky[y*w+x])
		}
		if sum/float64(n+1) >= minMeanSky {
			out = append(out, s)
		}
	}
	return out
}
