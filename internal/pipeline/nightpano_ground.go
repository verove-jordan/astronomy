package pipeline

// nightpano_ground.go puts the landscape back under the arch.
//
// Only the arch canvas can carry a foreground, and the reason is worth stating because it also
// explains why this is short. The ground does not move in azimuth and altitude, and the arch is drawn
// in azimuth and altitude — so once a panel's camera has been turned about the celestial pole by the
// sidereal angle between when it was shot and the instant the arch is drawn for
// (skypano.RotateAboutPole), the ordinary sky machinery projects the ground correctly and nothing
// else has to change. On the stereographic and galactic canvases there is no horizon at all, so
// there is nowhere for a foreground to go.
//
// Which panel supplies it is not a choice: it is whichever one was aimed lowest, because that is the
// only one with any ground in it. Everything here soft-fails — a panorama that lost its foreground is
// still a panorama, and a run must not fail over one.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/nightscape"
	"github.com/verove-jordan/astronomy/internal/skypano"
)

// archLayer is the landscape as it lands on the canvas: the reprojected linear foreground and, per
// pixel, how much of it the picture is (0 = pure sky, 1 = pure ground).
//
// It is returned rather than blended because the two layers must not meet until AFTER each has been
// through its own tone curve. See nightscape.StretchForeground: the landscape sits well over an order
// of magnitude below the sky glow it stands under, so the sky's curve renders it black.
type archLayer struct {
	Img    *fits.Image
	Weight []float32
	// White is the level the landscape's curve normalises by, measured on the SOURCE frame (whole
	// frame, sky included) and shifted into canvas units.
	//
	// Measuring it from Img instead is what turned the beach black. A percentile taken over the
	// landscape ALONE is set by the town's lights sitting along its horizon, which are four orders
	// brighter than sand; normalise by those and the sand divides down to nothing. gradeCompose
	// measures the whole frame, sky included, and that is the number that keeps a dark landscape
	// rendering as a dark landscape rather than as either black or a white slab.
	White float64
	// NoMeasure marks pixels the flatten and the grade must not SAMPLE. Those two measure the sky's
	// own level and colour, and a dark landscape inside their sample would drag the black point down
	// and grade the whole sky out washed.
	//
	// It is a separate mask rather than a hole punched in the coverage, and that distinction is the
	// whole bug it was written with: coverage also answers "is this pixel real", and the exporter
	// paints everything unreal BLACK. Zeroing the coverage under the landscape composited it
	// correctly and then blacked it out on the way to the PNG.
	NoMeasure []bool
}

// archForeground reprojects the lowest panel's ground onto the arch canvas.
func archForeground(opts Options, res *Result, panels []*panoPanel,
	cov []float32, c skypano.Canvas, lstEpoch float64) *archLayer {

	p := lowestPanel(panels)
	if p == nil {
		warnLive(opts, res, "nightpano: no panel carries a horizon, so the arch has no foreground")
		return nil
	}
	fg, err := fits.ReadImage(filepath.Join(p.outDir, "lin_fg.fits"))
	if err != nil {
		warnLive(opts, res, fmt.Sprintf("nightpano: panel %s has no linear foreground (%v)", p.label, err))
		return nil
	}
	alpha, err := fits.ReadImage(filepath.Join(p.outDir, "sky_alpha.fits"))
	if err != nil {
		warnLive(opts, res, fmt.Sprintf("nightpano: panel %s has no sky mask (%v)", p.label, err))
		return nil
	}
	// The linear layers are written BEFORE the display orientation and the stack AFTER it, so on a
	// portrait panel they arrive transposed and the shape check below refuses them. Apply the run's
	// own persisted transform first. (The same trap caught the detection mask; the lesson is that
	// anything reading lin_* or sky_alpha alongside the stack has to orient it.)
	if b, rerr := os.ReadFile(filepath.Join(p.outDir, "grade.orient")); rerr == nil && len(b) > 0 {
		mode := strings.TrimSpace(string(b))
		fg = nightscape.Orient(fg, mode)
		alpha = nightscape.Orient(alpha, mode)
	}
	if fg.W != p.img.W || fg.H != p.img.H || alpha.W != fg.W || alpha.H != fg.H {
		warnLive(opts, res, fmt.Sprintf(
			"nightpano: panel %s foreground is %dx%d against a %dx%d sky — not compositing something that "+
				"cannot be the same picture", p.label, fg.W, fg.H, p.img.W, p.img.H))
		return nil
	}

	// Turn the camera about the pole by the time between this panel and the instant being drawn.
	cam := skypano.RotateAboutPole(p.cam, lstEpoch-astro.LST(p.center.At, p.center.LonDeg))
	ro := skypano.DefaultRenderOptions()

	// NO photometric correction, deliberately. MatchPhotometry fits an ADDITIVE level match between
	// overlapping SKY regions — for the panel that carries the horizon it came to about -0.087, a
	// number sized to a sky background of 0.066. The landscape underneath sits at 0.002, so adding
	// that correction to it drives every pixel of the beach negative and the clamp then flattens the
	// whole thing to black. (Measured: it produced a white point of -0.034 and no stretch at all.)
	//
	// Nothing is lost by leaving it off. The correction exists to make panels agree in LINEAR light
	// before they are averaged into one sky; the landscape is never averaged with anything — it gets
	// its own curve and is composited in display space — so the level it arrives at is its own.
	fgImg, fgCov, err := skypano.Render([]skypano.Panel{{Name: p.label, Cam: cam, Img: fg}}, c, ro)
	if err != nil {
		warnLive(opts, res, fmt.Sprintf("nightpano: the foreground did not project (%v)", err))
		return nil
	}
	// The sky/ground mask travels the same way, so the two agree pixel for pixel about where the
	// ground is. Rendering it as an image rather than resampling it separately is what guarantees
	// that. It carries NO correction: it is an opacity, not light.
	maskImg, _, err := skypano.Render([]skypano.Panel{{Name: p.label, Cam: cam, Img: spread(alpha)}}, c, ro)
	if err != nil {
		warnLive(opts, res, fmt.Sprintf("nightpano: the sky mask did not project (%v)", err))
		return nil
	}

	out := make([]bool, len(cov))
	weight := make([]float32, len(cov))
	painted := 0
	for i := range out {
		if i >= len(fgCov) || fgCov[i] <= 0 {
			continue
		}
		// The mask is the SKY's opacity, so the ground is what is left of it — faded out at the panel's
		// own edge by its coverage.
		//
		// That second factor is not decoration. Render divides the accumulated colour by the summed
		// weight, so with a SINGLE panel the edge feather cancels itself out and the reprojected
		// foreground arrives at full strength right up to the frame boundary — where it would stop
		// dead, drawing a straight line across the picture. The coverage it returns is the
		// un-normalised weight, which is exactly the feather that was divided out, so putting it back
		// is what makes the landscape end softly.
		g := (1 - clamp01(float64(maskImg.Pix[0][i]))) * clamp01(float64(fgCov[i]))
		if g <= 0.01 {
			continue
		}
		weight[i] = float32(g)
		if g > 0.5 {
			out[i] = true // solidly landscape: real, kept, but not measured
		}
		painted++
	}
	if painted == 0 {
		warnLive(opts, res, fmt.Sprintf(
			"nightpano: panel %s was the lowest at %.0f degrees but no ground landed on the canvas",
			p.label, p.center.AltDeg))
		return nil
	}
	// The white point comes off the SOURCE frame, measured exactly the way gradeCompose measures it,
	// and the landscape is rendered in those same units — so no conversion stands between them.
	look := nightscape.LookByName(opts.Preset.Look)
	white := nightscape.ForegroundWhitePoint(fg, look.NormPercentile)
	if white <= 0 {
		// Never silent: StretchForeground declines a white point it cannot use, and a landscape that
		// was not stretched composites as black, which looks like a compositing bug rather than a
		// measurement one. It cost a whole run to learn that the first time.
		warnLive(opts, res, fmt.Sprintf(
			"nightpano: panel %s gave a white point of %.5g — its landscape cannot be stretched and is left out",
			p.label, white))
		return nil
	}
	opts.report(Progress{Line: fmt.Sprintf(
		"foreground from panel %s (aimed at %.0f degrees), %.1f%% of the canvas, white point %.5g",
		p.label, p.center.AltDeg, 100*float64(painted)/float64(len(out)), white)})
	return &archLayer{Img: fgImg, Weight: weight, White: white, NoMeasure: out}
}

// persistGradeInputs writes everything a render needs beside the canvas, so the look can be re-tuned
// without re-stacking: the coverage the grade measures through, the canvas geometry its band mask
// needs, and the landscape layer with its per-pixel weight. Best-effort — a panorama is not worth
// failing over a debugging artifact.
func persistGradeInputs(opts Options, res *Result, base string, img *fits.Image, cov []float32,
	c skypano.Canvas, ground *archLayer) {

	covImg := &fits.Image{W: img.W, H: img.H, C: 1, Pix: [][]float32{cov}}
	if err := covImg.WriteFITS(base + "_cov.fits"); err != nil {
		warnLive(opts, res, "nightpano: coverage not persisted — "+err.Error())
	}
	if b, err := json.Marshal(c); err == nil {
		if err := os.WriteFile(base+"_canvas.json", b, 0o644); err != nil {
			warnLive(opts, res, "nightpano: canvas geometry not persisted — "+err.Error())
		}
	}
	if ground == nil {
		return
	}
	if err := ground.Img.WriteFITS(base + "_fg.fits"); err != nil {
		warnLive(opts, res, "nightpano: landscape layer not persisted — "+err.Error())
	}
	w := &fits.Image{W: img.W, H: img.H, C: 1, Pix: [][]float32{ground.Weight}}
	if err := w.WriteFITS(base + "_fgweight.fits"); err != nil {
		warnLive(opts, res, "nightpano: landscape weight not persisted — "+err.Error())
	}
	if err := os.WriteFile(base+"_fgwhite.txt", []byte(fmt.Sprintf("%g\n", ground.White)), 0o644); err != nil {
		warnLive(opts, res, "nightpano: landscape white point not persisted — "+err.Error())
	}
}

// clearBelowHorizon zeroes the SKY's coverage under the horizon of an arch canvas, and reports how
// many pixels it took. Coverage rather than the pixels themselves: coverage is what every later stage
// reads to mean "this is sky I may measure and keep", so clearing it removes the smeared landscape
// from the background fit, from the grade's statistics, and from the exported picture in one move.
// It takes the landscape layer because the two horizons are not the same line. This one is geometric
// (altitude zero); the landscape's is the panel's own sky mask, from the phone's gravity vector, and
// it sits a little lower. Clearing the sky at the geometric line while the landscape only fades in at
// the lower one leaves a strip covered by NEITHER — and compositeGround, seeing no real sky there,
// fades the landscape's feather to black across it. That is the dark band between the sea and the sky.
//
// So where the landscape is partway in, the sky stays: the blend needs something real underneath it.
// Where the landscape is solid it is hidden anyway, and where there is no landscape at all the cut
// still has to happen or the smeared stacked ground comes back.
func clearBelowHorizon(cov []float32, c skypano.Canvas, ground *archLayer) int {
	if c.Fr != skypano.Horizon || c.W <= 0 || len(cov) != c.W*c.H {
		return 0
	}
	var w []float32
	if ground != nil && len(ground.Weight) == len(cov) {
		w = ground.Weight
	}
	n := 0
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			i := y*c.W + x
			if cov[i] <= 0 {
				continue
			}
			// Off the projection counts as below it: those pixels are past the antipode, under the
			// observer's feet.
			alt, ok := c.AltitudeAt(float64(x)+0.5, float64(y)+0.5)
			if ok && alt >= 0 {
				continue
			}
			if w != nil && w[i] > 0 && w[i] < 1 {
				continue // the landscape is fading in here; leave it something to fade into
			}
			cov[i] = 0
			n++
		}
	}
	return n
}

// compositeGround mixes the already-stretched landscape into the already-graded sky, and marks the
// result as picture so the exporter keeps it.
//
// What the landscape fades INTO at the panel's edge decides whether that edge is invisible or a pale
// frame around the whole thing, and under the horizon it must not fade into the canvas. Coverage
// there has been cleared (see clearBelowHorizon), but clearing coverage does not erase pixels: the
// canvas still holds the smeared, stacked copy of the beach that Render put there, and it is merely
// no longer marked as sky. Blending toward it would paint that smear back in around every edge of
// the clean landscape — which is exactly the bright frame this used to draw. So: where the sky is
// real the two mix; where it is not, the landscape stands alone and its own weight fades it to the
// black the exporter paints everywhere else.
//
// keep is read before it is written — it is the only record of which pixels had a real sky — so the
// marking is a separate pass.
func compositeGround(img *fits.Image, keep []bool, g *archLayer) {
	if img == nil || g == nil || g.Img == nil || len(g.Weight) != img.W*img.H || len(keep) != img.W*img.H {
		return
	}
	for i, w := range g.Weight {
		if w <= 0 {
			continue
		}
		skyIsReal := keep[i]
		for ch := 0; ch < img.C && ch < g.Img.C; ch++ {
			gv := g.Img.Pix[ch][i]
			if skyIsReal {
				img.Pix[ch][i] = (1-w)*img.Pix[ch][i] + w*gv
			} else {
				img.Pix[ch][i] = w * gv
			}
		}
	}
	for i, w := range g.Weight {
		if w > 0 {
			keep[i] = true
		}
	}
}

// lowestPanel is the one aimed nearest the horizon — the only one that can hold any ground.
func lowestPanel(panels []*panoPanel) *panoPanel {
	var best *panoPanel
	for _, p := range panels {
		if p.outDir == "" || p.img == nil || !p.center.HasSite || !p.center.HasTime {
			continue
		}
		if best == nil || p.center.AltDeg < best.center.AltDeg {
			best = p
		}
	}
	return best
}

// spread turns a single-plane mask into the three-plane image Render expects.
func spread(m *fits.Image) *fits.Image {
	if m.C >= 3 {
		return m
	}
	out := fits.NewImage(m.W, m.H, 3)
	for c := 0; c < 3; c++ {
		copy(out.Pix[c], m.Pix[0])
	}
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
