package solar

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// seqpanel_live_test.go measures the panel masters a finished eclipse sheet persisted, one panel per
// line, and it exists because the single figure a run records for a panel — MeasureSharpness —
// silently chooses between two probes with very different failure modes. It prefers the OCCULTER's
// edge and falls back to the solar limb, so a panel whose lunar limb is barely against the crescent
// is graded on a handful of wedges near the cusps while a panel at maximum is graded on the whole
// arc. Two panels' sigmas are then not the same measurement, and comparing them ranks the sheet
// wrongly.
//
// So both probes are reported side by side, with the number of wedges each could actually measure
// and the spread over them. Opt-in:
//
//	ASTRO_SEQ_DIR='output/<target>/<run>/sequence' go test ./internal/solar -run SeqPanelPSF_Live -v
func TestSeqPanelPSF_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SEQ_DIR")
	if dir == "" {
		t.Skip("set ASTRO_SEQ_DIR=<run dir>/sequence to measure a finished sheet's panels")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("..", "..", dir)
	}
	names, err := panelMasters(dir)
	require.NoError(t, err)
	require.NotEmpty(t, names, "no panel_NN.fits in %s", dir)

	var b strings.Builder
	fmt.Fprintf(&b, "\n%-16s %5s %6s   %-22s   %-22s\n",
		"panel", "obsc", "sharp", "occulter edge", "solar limb")
	fmt.Fprintf(&b, "%-16s %5s %6s   %5s %5s %5s %5s   %5s %5s %5s %5s\n",
		"", "", "", "n", "p10", "med", "p90", "n", "p10", "med", "p90")
	for _, n := range names {
		im, err := fits.ReadImage(filepath.Join(dir, n))
		require.NoError(t, err)
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		g, ok := FitGeometry(mono, true)
		if !ok {
			fmt.Fprintf(&b, "%-16s  no geometry could be fitted\n", n)
			continue
		}
		occ := sectorWidths(mono, g.Moon, edgeRising)
		limb := sectorWidths(mono, g.Sun, edgeFalling)
		fmt.Fprintf(&b, "%-16s %5.1f %6.2f   %5d %5s %5s %5s   %5d %5s %5s %5s\n",
			n, g.Obscuration*100, MeasureSharpness(mono, g).SigmaPx,
			len(occ), pct(occ, 0.10), pct(occ, 0.50), pct(occ, 0.90),
			len(limb), pct(limb, 0.10), pct(limb, 0.50), pct(limb, 0.90))
	}
	t.Log(b.String())
}

func panelMasters(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "panel_") && strings.HasSuffix(e.Name(), ".fits") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// sectorWidths is MeasureEdge's own loop with the widths handed back instead of their median, so the
// test can report how many wedges carried an edge at all and how far they disagreed.
func sectorWidths(im *fits.Image, l Limb, dir edgeDirection) []float64 {
	return sectorWidthsFiltered(im, l, dir, func(int) bool { return true })
}

// sunClearOfTheMoon keeps the wedges of the SOLAR limb whose profile is Sun all the way in — the
// mirror of sunBehindTheEdge, so the two probes can be compared on equal terms rather than one of
// them being asked to measure across a cusp.
func sunClearOfTheMoon(g Pair) func(int) bool {
	return func(sector int) bool {
		if g.Moon.R <= 0 {
			return true
		}
		a := sectorMidAngle(sector)
		cos, sin := math.Cos(a), math.Sin(a)
		for _, r := range []float64{g.Sun.R, g.Sun.R - psfHalfPx} {
			x, y := g.Sun.CX+r*cos, g.Sun.CY+r*sin
			if math.Hypot(x-g.Moon.CX, y-g.Moon.CY) <= g.Moon.R {
				return false
			}
		}
		return true
	}
}

func sectorWidthsFiltered(im *fits.Image, l Limb, dir edgeDirection, keep func(int) bool) []float64 {
	var out []float64
	all := sectorWidthsBySector(im, l, dir)
	for s, w := range all {
		if w > 0 && keep(s) {
			out = append(out, w)
		}
	}
	sort.Float64s(out)
	return out
}

// sectorWidthsBySector is sectorWidths keyed by wedge, so a filter can be applied afterwards.
func sectorWidthsBySector(im *fits.Image, l Limb, dir edgeDirection) []float64 {
	out := make([]float64, psfSectors)
	if l.R <= 0 {
		return out
	}
	prof := make([]float64, 2*int(psfHalfPx/psfStepPx)+1)
	for s := 0; s < psfSectors; s++ {
		if !sectorProfile(im, l, s, prof) {
			continue
		}
		if dir == edgeRising {
			reverseProfile(prof)
		}
		sigma, _, ok := edgeWidth(prof)
		if !ok || sigma < psfSigmaMin || sigma > psfSigmaMax {
			continue
		}
		out[s] = sigma
	}
	return out
}

func pct(sorted []float64, q float64) string {
	if len(sorted) == 0 {
		return "-"
	}
	i := int(q * float64(len(sorted)-1))
	return fmt.Sprintf("%.2f", sorted[i])
}

// TestSeqSourceSharpness_Live profiles the material a sheet was actually built from: every Nth frame
// a run extracted from one clip, measured the same way a panel master is, so "this panel is soft"
// can be told apart from "the recording is soft here". Opt-in:
//
//	ASTRO_SEQ_SCRATCH='work/sun_<runID>' ASTRO_SEQ_CLIP=08122026202918901 ASTRO_SEQ_STRIDE=10 \
//	  go test ./internal/solar -run SeqSourceSharpness_Live -v -timeout 40m
func TestSeqSourceSharpness_Live(t *testing.T) {
	scratch := os.Getenv("ASTRO_SEQ_SCRATCH")
	clip := os.Getenv("ASTRO_SEQ_CLIP")
	if scratch == "" || clip == "" {
		t.Skip("set ASTRO_SEQ_SCRATCH=<work>/sun_<runID> and ASTRO_SEQ_CLIP=<clip stem> to profile a clip")
	}
	if !filepath.IsAbs(scratch) {
		scratch = filepath.Join("..", "..", scratch)
	}
	stride := 10
	if v := os.Getenv("ASTRO_SEQ_STRIDE"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		require.Positive(t, n)
		stride = n
	}
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	var frames []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), clip) && strings.HasSuffix(e.Name(), ".fits") {
			frames = append(frames, e.Name())
		}
	}
	sort.Strings(frames)
	require.NotEmpty(t, frames, "no extracted frame of %s in %s", clip, scratch)

	var b strings.Builder
	fmt.Fprintf(&b, "\n%d frames extracted from %s, measuring every %d\n", len(frames), clip, stride)
	fmt.Fprintf(&b, "%-10s %7s %6s %8s %6s %5s %9s %6s %5s\n",
		"index", "sunR", "obsc", "SELECTED", "limb", "limbN", "cleanLimb", "occ", "occN")
	for i := 0; i < len(frames); i += stride {
		n := frames[i]
		im, err := fits.ReadImage(filepath.Join(scratch, n))
		if err != nil {
			continue
		}
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		g, ok := FitGeometry(mono, true)
		if !ok {
			fmt.Fprintf(&b, "%-10s  no geometry\n", frameIndexOf(n, clip))
			continue
		}
		limb := MeasurePSF(mono, g.Sun)
		occ := sectorWidths(mono, g.Moon, edgeRising)
		occSigma := "-"
		if e := MeasureEdge(mono, g.Moon, edgeRising, sunAngularDiameterArcsec/(2*g.Sun.R)); e.OK {
			occSigma = fmt.Sprintf("%.2f", e.SigmaPx)
		}
		clean := sectorWidthsFiltered(mono, g.Sun, edgeFalling, sunClearOfTheMoon(g))
		fmt.Fprintf(&b, "%-10s %7.1f %6.1f %8.2f %6.2f %5d %4s(%2d) %6s %5d\n",
			frameIndexOf(n, clip), g.Sun.R, g.Obscuration*100,
			MeasureSharpness(mono, g).SigmaPx, limb.SigmaPx, len(sectorWidths(mono, g.Sun, edgeFalling)),
			pct(clean, 0.50), len(clean), occSigma, len(occ))
	}
	t.Log(b.String())
}

func frameIndexOf(name, clip string) string {
	return strings.TrimSuffix(strings.TrimPrefix(name, clip+"_MOV_"), ".fits")
}

// TestSeqExportFrames_Live writes named extracted frames out as PNGs, normalised against the
// crescent's own level, so two candidates a metric disagrees about can simply be looked at. Opt-in:
//
//	ASTRO_SEQ_SCRATCH='work/sun_<runID>' ASTRO_SEQ_CLIP=<stem> ASTRO_SEQ_INDICES=00451,01432 \
//	  ASTRO_SEQ_OUT=/tmp/x go test ./internal/solar -run SeqExportFrames_Live -v
func TestSeqExportFrames_Live(t *testing.T) {
	scratch, clip := os.Getenv("ASTRO_SEQ_SCRATCH"), os.Getenv("ASTRO_SEQ_CLIP")
	indices, out := os.Getenv("ASTRO_SEQ_INDICES"), os.Getenv("ASTRO_SEQ_OUT")
	if scratch == "" || clip == "" || indices == "" || out == "" {
		t.Skip("set ASTRO_SEQ_SCRATCH, ASTRO_SEQ_CLIP, ASTRO_SEQ_INDICES and ASTRO_SEQ_OUT to export frames")
	}
	if !filepath.IsAbs(scratch) {
		scratch = filepath.Join("..", "..", scratch)
	}
	for _, idx := range strings.Split(indices, ",") {
		idx = strings.TrimSpace(idx)
		name := fmt.Sprintf("%s_MOV_%s.fits", clip, idx)
		im, err := fits.ReadImage(filepath.Join(scratch, name))
		require.NoError(t, err)
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		g, ok := FitGeometry(mono, true)
		require.True(t, ok, "no geometry for %s", name)
		level := CrescentLevel(mono, g)
		require.Positive(t, level)
		shown := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{make([]float32, im.W*im.H)}}
		for i, v := range mono.Pix[0] {
			shown.Pix[0][i] = float32(math.Min(1, math.Max(0, float64(v)/(1.05*level))))
		}
		dst := filepath.Join(out, fmt.Sprintf("%s_%s.png", clip, idx))
		require.NoError(t, WritePNG(shown, dst))
		t.Logf("%s  obsc %.1f%%  occulter %.2f  limb %.2f -> %s",
			idx, g.Obscuration*100, MeasureSharpness(mono, g).SigmaPx, MeasurePSF(mono, g.Sun).SigmaPx, dst)
	}
}

// sectorMidAngle is the azimuth through the middle of one of MeasureEdge's wedges.
func sectorMidAngle(sector int) float64 {
	return 2 * math.Pi * (float64(sector) + 0.5) / float64(psfSectors)
}
