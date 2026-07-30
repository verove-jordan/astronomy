package pipeline

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
)

func fullCoverage(w, h int) *coverageGrid {
	g := &coverageGrid{W: (w + 7) / 8, H: (h + 7) / 8, Scale: 8, Canvas: canvasSpec{W: w, H: h}, Frames: 5}
	g.Counts = make([]uint16, g.W*g.H)
	for i := range g.Counts {
		g.Counts[i] = 5
	}
	return g
}

// unionChannel builds a ChannelResult + on-disk union master whose pixel at (x,y) is x+100y, with
// the anchor frame rectangle recorded on the canvas.
func unionChannel(t *testing.T, dir, filter string, w, h, offX, offY, aw, ah int) (ChannelResult, string) {
	t.Helper()
	path := writeHaTestFITS(t, dir, "master_"+filterTag(filter)+".fits", w, h, func(x, y int) float32 {
		return float32(x + 100*y)
	})
	return ChannelResult{Filter: filter, OutputPath: path,
		Canvas:   &CanvasInfo{W: w, H: h, OffX: offX, OffY: offY, AnchorW: aw, AnchorH: ah},
		coverage: fullCoverage(w, h)}, path
}

// TestMosaicHarmonize_AbsolutePaths pins the job-367 regression: channelMastersMap hands harmonize
// ABSOLUTE master paths; padding must read them as-is (never outDir+path+".fits") and repoint the
// map to absolute padded files of one common canvas.
func TestMosaicHarmonize_AbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	chR, pR := unionChannel(t, dir, "R", 32, 24, 4, 5, 20, 16)
	chHa, pHa := unionChannel(t, dir, "Ha", 28, 26, 6, 7, 20, 16)
	res := &Result{Channels: []ChannelResult{chR, chHa}}
	masters := map[string]string{"R": pR, "Ha": pHa}
	opts := Options{Preset: &mode.Preset{Mosaic: true, MosaicFill: "fill"}}

	out := mosaicHarmonize(opts, res, masters, dir)

	// Common canvas: anchor coords x∈[-6,28) y∈[-7,19) → 34×26; R lands at (2,2), Ha at (0,0).
	for f, wantLeft := range map[string]int{"R": 2, "Ha": 0} {
		p := out[f]
		if !filepath.IsAbs(p) {
			t.Fatalf("%s entry not absolute: %q", f, p)
		}
		im, err := fits.ReadImage(p)
		if err != nil {
			t.Fatalf("read harmonized %s: %v", f, err)
		}
		if im.W != 34 || im.H != 26 {
			t.Fatalf("%s harmonized to %dx%d, want 34x26", f, im.W, im.H)
		}
		wantTop := map[string]int{"R": 2, "Ha": 0}[f]
		// Content probe: original (10,10)=1010 must sit at (left+10, top+10).
		got := im.Pix[0][(wantTop+10)*im.W+wantLeft+10]
		if math.Abs(float64(got)-1010) > 0.01 {
			t.Fatalf("%s content misplaced: probe=%v, want 1010", f, got)
		}
	}
	for i := range res.Channels {
		c := res.Channels[i].Canvas
		if c == nil || c.W != 34 || c.H != 26 || c.OffX != 6 || c.OffY != 7 || c.AnchorW != 20 || c.AnchorH != 16 {
			t.Fatalf("channel %s canvas after harmonize = %+v", res.Channels[i].Filter, c)
		}
	}
}

// TestMosaicHarmonize_RevertOnAnchorChannel: when one channel stayed on the anchor canvas, every
// union channel is cropped back to its exact anchor rectangle so the combine sees identical dims.
func TestMosaicHarmonize_RevertOnAnchorChannel(t *testing.T) {
	dir := t.TempDir()
	chR, pR := unionChannel(t, dir, "R", 32, 24, 4, 5, 20, 16)
	pG := writeHaTestFITS(t, dir, "master_g.fits", 20, 16, func(x, y int) float32 { return 1 })
	chG := ChannelResult{Filter: "G", OutputPath: pG} // no Canvas/coverage: anchor-canvas channel
	res := &Result{Channels: []ChannelResult{chR, chG}}
	masters := map[string]string{"R": pR, "G": pG}
	opts := Options{Preset: &mode.Preset{Mosaic: true}}

	out := mosaicHarmonize(opts, res, masters, dir)

	im, err := fits.ReadImage(out["R"])
	if err != nil {
		t.Fatalf("read reverted R: %v", err)
	}
	if im.W != 20 || im.H != 16 {
		t.Fatalf("reverted R is %dx%d, want the 20x16 anchor frame", im.W, im.H)
	}
	// Anchor origin sat at (4,5) on the union: reverted (0,0) must hold 4+100·5.
	if got := im.Pix[0][0]; math.Abs(float64(got)-504) > 0.01 {
		t.Fatalf("reverted R misplaced: (0,0)=%v, want 504", got)
	}
	if out["G"] != pG {
		t.Fatalf("anchor-canvas channel must stay untouched, got %q", out["G"])
	}
	if res.Channels[0].Canvas != nil {
		t.Fatal("reverted channel must drop its union canvas record")
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "mosaic abandoned") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a loud 'mosaic abandoned' warning, got %v", res.Warnings)
	}
}
