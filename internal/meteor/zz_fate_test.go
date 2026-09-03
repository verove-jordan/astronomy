package meteor

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/starfield"
	"github.com/verove-jordan/astronomy/internal/trail"
)

// TestZZFate answers one question about a real panel: of the transients that crossed the field, how
// many did the sigma-clip REJECT and how many SURVIVED into the clean stack?
//
// The question matters because it decides what the meteor work is for. If the clip deletes them all,
// the job is to recover them from the rejected layer. If most survive, the job is much smaller and
// aimed only at the ones that did not.
//
// Ground truth comes from the registered frames themselves, not from either output. A transient is
// unique to ONE frame, so subtracting the pixelwise MINIMUM of its two neighbours leaves it standing
// while the static sky cancels — the minimum rather than the mean because a neighbour with its own
// transient would otherwise leak into the residual.
//
//	ASTRO_FATE_SEQ=<work/.../01_seq> ASTRO_FATE_RUN=<output/.../runID> \
//	  go test ./internal/meteor/ -run TestZZFate -v
func TestZZFate(t *testing.T) {
	seqDir, runDir := os.Getenv("ASTRO_FATE_SEQ"), os.Getenv("ASTRO_FATE_RUN")
	if seqDir == "" || runDir == "" {
		t.Skip("set ASTRO_FATE_SEQ and ASTRO_FATE_RUN")
	}
	frames, _ := filepath.Glob(filepath.Join(seqDir, "r_light_*.fits"))
	sort.Strings(frames)
	if len(frames) < 3 {
		t.Skip("need at least 3 registered frames")
	}
	sky, err := fits.ReadImage(filepath.Join(runDir, "lin_sky.fits"))
	if err != nil {
		t.Skip(err)
	}
	meta, err := fits.ReadImage(filepath.Join(runDir, "transients_meta.fits"))
	if err != nil {
		t.Skip(err)
	}
	voteFrac := 0.06 // meteors span about a tenth of the frame, not the satellite-tuned quarter
	if v := os.Getenv("ASTRO_FATE_VOTEFRAC"); v != "" {
		fmt.Sscanf(v, "%f", &voteFrac)
	}
	w, h := sky.W, sky.H
	n := len(frames)
	fmt.Printf("%d registered frames, stack %dx%d\n", n, w, h)

	lum := func(path string) []float32 {
		im, e := fits.ReadImage(path)
		if e != nil || im == nil {
			return nil
		}
		return im.Pix[1] // green, as the stack's own luminance
	}
	skyLum := sky.Pix[1]

	// Blank the stars before looking for lines. Registration never cancels them exactly — the frames
	// drift and interpolate differently — so every star leaves residue in a difference image, and that
	// residue floods the Hough accumulator until no real line can dominate it. The positions are not
	// guesswork: they are measured in the clean stack, which is the same sky.
	starMask := make([]bool, w*h)
	det := starfield.Detect(skyLum, w, h, starfield.Options{Sigma: 5, BoxRadius: 5, MinSeparation: 6, Max: 20000})
	const rad = 9
	for _, st := range det {
		cx, cy := int(st.X), int(st.Y)
		for dy := -rad; dy <= rad; dy++ {
			for dx := -rad; dx <= rad; dx++ {
				if dx*dx+dy*dy > rad*rad {
					continue
				}
				x, y := cx+dx, cy+dy
				if x >= 0 && y >= 0 && x < w && y < h {
					starMask[y*w+x] = true
				}
			}
		}
	}
	// Registration leaves a rim where only part of the sequence reached, and that rim is a straight
	// bright edge running the whole length of the frame — the strongest line in the picture, and never
	// a meteor. Blank a margin so the Hough cannot lock onto it.
	const margin = 160
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < margin || y < margin || x >= w-margin || y >= h-margin {
				starMask[y*w+x] = true
			}
		}
	}
	masked := 0
	for _, m := range starMask {
		if m {
			masked++
		}
	}
	fmt.Printf("%d stars masked, covering %.1f%% of the frame\n", len(det), 100*float64(masked)/float64(w*h))

	type found struct {
		frame                    int
		seg                      trail.Segment
		amp                      float64 // single-frame amplitude above the local sky
		rejectedFrac, inStackAmp float64
	}
	var all []found
	// fluxCut selects pixels bright enough to be a transient rather than noise: the streaks in the
	// contact sheet sit far above the residual's own spread.
	fluxCut := 0.03
	if v := os.Getenv("ASTRO_FATE_FLUXCUT"); v != "" {
		fmt.Sscanf(v, "%f", &fluxCut)
	}
	// Several cuts in one pass: the rejection fraction MUST rise with brightness, because the clip
	// rejects what exceeds the sky by more. A flat curve would mean the statistic is measuring
	// something other than transients.
	cuts := []float64{0.02, 0.03, 0.05, 0.10, 0.20}
	totalAt := make([]int, len(cuts))
	rejAt := make([]int, len(cuts))
	totalFlux, rejectedFlux := 0, 0

	// A contact sheet of every frame's own residual, so "is there a meteor here at all" is a question
	// that can be looked at rather than inferred from a detector that might simply be refusing.
	const cols, tileW, tileH = 6, 336, 252
	rows := (n - 2 + cols - 1) / cols
	sheet := image.NewGray(image.Rect(0, 0, cols*tileW, rows*tileH))

	prev, cur := lum(frames[0]), lum(frames[1])
	for f := 1; f < n-1; f++ {
		next := lum(frames[f+1])
		if prev == nil || cur == nil || next == nil {
			break
		}
		// What is unique to THIS frame.
		resid := make([]float32, w*h)
		for i := range resid {
			m := prev[i]
			if next[i] < m {
				m = next[i]
			}
			if starMask[i] {
				continue
			}
			if d := cur[i] - m; d > 0 {
				resid[i] = d
			}
		}
		if sheet != nil {
			addTile(sheet, resid, w, h, f-1, tileW, tileH, cols)
		}
		// The aggregate that needs no detector: over the pixels carrying real transient flux in THIS
		// frame, how many did the clip actually reject? Rejected pixels are exactly those where the
		// transient layer records this frame as the one it threw away.
		for i, e := range resid {
			if starMask[i] {
				continue
			}
			rejected := int(meta.Pix[0][i]) == f
			for k, c := range cuts {
				if float64(e) >= c {
					totalAt[k]++
					if rejected {
						rejAt[k]++
					}
				}
			}
			if float64(e) >= fluxCut {
				totalFlux++
				if rejected {
					rejectedFlux++
				}
			}
		}
		prev, cur = cur, next
	}

	if sheet != nil {
		out := filepath.Join(runDir, "frame_residuals.png")
		if f, e := os.Create(out); e == nil {
			_ = png.Encode(f, sheet)
			f.Close()
			fmt.Println("contact sheet ->", out)
		}
	}
	fmt.Printf("\n%-10s %-12s %-12s %s\n", "cut", "px", "rejected", "survived")
	for k, c := range cuts {
		if totalAt[k] == 0 {
			continue
		}
		fmt.Printf("%-10.2f %-12d %-12.1f %.1f%%\n", c, totalAt[k],
			100*float64(rejAt[k])/float64(totalAt[k]), 100*float64(totalAt[k]-rejAt[k])/float64(totalAt[k]))
	}
	if totalFlux > 0 {
		fmt.Printf("\ntransient flux above %.3f, away from stars: %d px, of which %d (%.1f%%) were REJECTED by the clip; %.1f%% SURVIVED into the stack\n",
			fluxCut, totalFlux, rejectedFlux, 100*float64(rejectedFlux)/float64(totalFlux),
			100*float64(totalFlux-rejectedFlux)/float64(totalFlux))
	} else {
		fmt.Printf("\nno pixels above %.3f away from stars\n", fluxCut)
	}
	if len(all) == 0 {
		fmt.Println("(no individual streaks isolated — see the note in the report)")
		return
	}
	sort.Slice(all, func(a, b int) bool { return all[a].amp > all[b].amp })
	fmt.Printf("\n%-6s %-8s %-9s %-11s %-11s %s\n",
		"frame", "amp", "len(px)", "rejected%", "in stack", "verdict")
	rejected, survived, partial := 0, 0, 0
	for _, x := range all {
		expected := x.amp / float64(n) // what a fully-surviving transient contributes to the mean
		ratio := 0.0
		if expected > 0 {
			ratio = x.inStackAmp / expected
		}
		verdict := "PARTIAL"
		switch {
		case x.rejectedFrac > 0.6 && ratio < 0.4:
			verdict, rejected = "rejected", rejected+1
		case x.rejectedFrac < 0.25 && ratio > 0.5:
			verdict, survived = "SURVIVED", survived+1
		default:
			partial++
		}
		fmt.Printf("%-6d %-8.4f %-9.0f %-11.0f %-11.4f %s (%.0f%% of the %.4f a survivor would leave)\n",
			x.frame, x.amp, x.seg.T1-x.seg.T0, 100*x.rejectedFrac, x.inStackAmp, verdict, 100*ratio, expected)
	}
	fmt.Printf("\n%d transients: %d rejected by the clip, %d survived into the stack, %d partial\n",
		len(all), rejected, survived, partial)
}

// fate measures one segment three ways: how bright it was in its own frame, how much of its track the
// clip threw away, and how much of it reached the clean stack.
func fate(seg trail.Segment, resid []float32, meta *fits.Image, skyLum []float32, w, h, frame int) (amp, rejectedFrac, inStack float64) {
	var on []int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if seg.Contains(float64(x), float64(y)) {
				on = append(on, y*w+x)
			}
		}
	}
	if len(on) == 0 {
		return 0, 0, 0
	}
	// The track's own amplitude: a high quantile of the residual along it, so a few hot pixels do not
	// set the number and the faint half does not drag it down.
	amp = quantileAt(resid, on, 0.9)

	// How much of the track the clip rejected IN THIS FRAME. meta plane 0 is the frame index of the
	// brightest rejection at that pixel.
	rej := 0
	for _, i := range on {
		if int(meta.Pix[0][i]) == frame {
			rej++
		}
	}
	rejectedFrac = float64(rej) / float64(len(on))

	// What reached the stack: the track's level above the sky just beside it.
	inStack = quantileAt(skyLum, on, 0.9) - localBackground(seg, skyLum, w, h)
	if inStack < 0 {
		inStack = 0
	}
	return amp, rejectedFrac, inStack
}

// localBackground samples the stack in a band beside the track, so the comparison is against the sky
// the streak actually sits on rather than the whole frame.
func localBackground(seg trail.Segment, plane []float32, w, h int) float64 {
	var v []float32
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// perpDist and project are unexported on trail.Segment; the fields are not.
			d := math.Abs(seg.Nx*float64(x) + seg.Ny*float64(y) - seg.C)
			if d < 3*seg.Width || d > 8*seg.Width {
				continue
			}
			t := -seg.Ny*float64(x) + seg.Nx*float64(y)
			if t < seg.T0 || t > seg.T1 {
				continue
			}
			v = append(v, plane[y*w+x])
		}
	}
	if len(v) == 0 {
		return 0
	}
	sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
	return float64(v[len(v)/2])
}

// addTile draws one downscaled, hard-stretched residual into the contact sheet.
func addTile(sheet *image.Gray, resid []float32, w, h, idx, tw, th, cols int) {
	v := make([]float32, 0, w*h/64)
	for i := 0; i < len(resid); i += 64 {
		v = append(v, resid[i])
	}
	sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
	hi := v[int(0.9995*float64(len(v)-1))]
	if hi <= 0 {
		hi = 1
	}
	ox, oy := (idx%cols)*tw, (idx/cols)*th
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			// Max over the source block, so a one-pixel-wide streak survives the downscale.
			var m float32
			for by := y * h / th; by < (y+1)*h/th && by < h; by++ {
				for bx := x * w / tw; bx < (x+1)*w/tw && bx < w; bx++ {
					if e := resid[by*w+bx]; e > m {
						m = e
					}
				}
			}
			g := math.Sqrt(math.Min(float64(m/hi), 1))
			sheet.SetGray(ox+x, oy+y, color.Gray{uint8(255 * g)})
		}
	}
}

func quantileAt(plane []float32, idx []int, q float64) float64 {
	v := make([]float32, len(idx))
	for i, k := range idx {
		v[i] = plane[k]
	}
	sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
	return float64(v[int(q*float64(len(v)-1))])
}
