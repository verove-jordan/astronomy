package pipeline

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
	"github.com/verove-jordan/astronomy/internal/noise"
)

// The raw Ha master is continuum + emission: stars, the galaxy disc and the sky pedestal all sit in
// the layer that gets screened PURE RED, so the black-point clip and the low screen opacity that keep
// them from painting the frame red also erase the faint emission sitting just above them. Subtracting
// the scaled broadband continuum first (excess = Ha − k·R) leaves a layer that is black everywhere
// except true Ha emission — the stretch can then lift faint HII structure hard, and the screen shows
// filaments instead of a dim wash.
const (
	// haKSelectLo/Hi bound the reference percentile band whose pixels calibrate k: bright enough to
	// be continuum-dominated (star cores + disc), below the possibly-clipped extreme top.
	haKSelectLo = 85.0
	haKSelectHi = 99.9
	// haKMinSamples guards the ratio estimate — with fewer continuum-bright pixels the median ratio
	// is star-shot noise, not a continuum scale.
	haKMinSamples = 10_000
	// haFaintSigma: an excess layer whose P99.5 sits below this many Ha-noise sigmas carries no real
	// emission; screening it would only paint stretched noise red.
	haFaintSigma = 2.0
	// haExcessBase names the emission-only FITS written next to the other stretch inputs.
	haExcessBase = "ha_excess"
)

// haContinuum is the outcome of the subtraction: the emission-only FITS to stretch in place of the
// raw Ha master, plus the fit provenance for the run notes.
type haContinuum struct {
	ExcessPath string
	K          float64 // continuum scale: excess = Ha − K·ref
	Ref        string  // reference filter used ("R", else "L")
	Faint      bool    // excess indistinguishable from noise — the caller should drop the Ha screen
	P995Sigma  float64 // P99.5(excess) in units of the raw Ha layer's noise sigma
}

// resolveChannelFITS maps a channel-map entry (a Siril-relative base name like "combine_Ha", or an
// absolute path) onto the FITS file it names on disk. Siril `load` resolves bare names against the
// script's working dir and appends the FITS extension itself — a Go-side reader must do the same or
// it silently misses every relative entry (the star-quality bare-name lesson, relearned on job 366).
func resolveChannelFITS(workDir, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(workDir, p)
	}
	if filepath.Ext(p) != "" {
		return p
	}
	for _, ext := range []string{".fits", ".fit", ".fts"} {
		if _, err := os.Stat(p + ext); err == nil {
			return p + ext
		}
	}
	return p + ".fits"
}

// haContinuumSubtract builds the continuum-subtracted Ha excess layer: excess = clamp(Ha − k·ref, 0)
// with k the median Ha/ref ratio over continuum-bright reference pixels, so stars and broadband
// structure cancel out and only true Ha emission survives. Channel entries are resolved against
// workDir (the Siril run dir); the excess FITS is written into writeDir. It returns (nil, reason)
// when the subtraction cannot run — the caller then screens the raw Ha layer exactly as before.
func haContinuumSubtract(workDir string, channels map[string]string, writeDir string) (*haContinuum, string) {
	// R shares the Ha band; L still scales linearly with continuum.
	return lineContinuumSubtract(workDir, channels, writeDir, "Ha", []string{"R", "L"}, haExcessBase)
}

// oiiiContinuumSubtract is the OIII twin: the [OIII] 500.7 nm line sits in the G/B overlap, so the
// continuum is estimated from G first, then B, then L.
func oiiiContinuumSubtract(workDir string, channels map[string]string, writeDir string) (*haContinuum, string) {
	return lineContinuumSubtract(workDir, channels, writeDir, "OIII", []string{"G", "B", "L"}, "oiii_excess")
}

// siiContinuumSubtract is the SII twin: [SII] 671.6/673.1 nm sits inside the R band (deeper red than
// Hα 656 nm), so it takes the same reference order as Ha — R first, then L.
func siiContinuumSubtract(workDir string, channels map[string]string, writeDir string) (*haContinuum, string) {
	return lineContinuumSubtract(workDir, channels, writeDir, "SII", []string{"R", "L"}, "sii_excess")
}

// lineContinuumSubtract is the shared engine behind the per-line wrappers: excess = clamp(line − k·ref, 0).
func lineContinuumSubtract(workDir string, channels map[string]string, writeDir, line string, refOrder []string, outBase string) (*haContinuum, string) {
	refFilter := ""
	for _, f := range refOrder {
		if _, ok := channels[f]; ok {
			refFilter = f
			break
		}
	}
	if refFilter == "" {
		return nil, fmt.Sprintf("no broadband channel (%s) to estimate the continuum from", strings.Join(refOrder, "/"))
	}
	ha, err := fits.ReadImage(resolveChannelFITS(workDir, channels[line]))
	if err != nil {
		return nil, "read " + line + " master: " + err.Error()
	}
	ref, err := fits.ReadImage(resolveChannelFITS(workDir, channels[refFilter]))
	if err != nil {
		return nil, "read " + refFilter + " master: " + err.Error()
	}
	if ha.W != ref.W || ha.H != ref.H || len(ha.Pix) == 0 || len(ref.Pix) == 0 {
		return nil, fmt.Sprintf("%s %dx%d vs %s %dx%d — masters not co-registered",
			line, ha.W, ha.H, refFilter, ref.W, ref.H)
	}
	hp, rp := ha.Pix[0], ref.Pix[0]

	refSub := imgops.Subsample(rp, 200_000)
	lo := imgops.Percentile(refSub, haKSelectLo)
	hi := imgops.Percentile(refSub, haKSelectHi)
	if !(lo > 1e-6) || !(hi > lo) {
		return nil, fmt.Sprintf("%s reference too dark/flat to calibrate the continuum (P%.0f=%.2g)", refFilter, haKSelectLo, lo)
	}
	stride := len(rp)/2_000_000 + 1
	ratios := make([]float32, 0, 300_000)
	for i := 0; i < len(rp); i += stride {
		r := rp[i]
		if float64(r) > lo && float64(r) < hi {
			ratios = append(ratios, hp[i]/r)
		}
	}
	if len(ratios) < haKMinSamples {
		return nil, fmt.Sprintf("only %d continuum-bright pixels — cannot fit a stable %s/%s scale", len(ratios), line, refFilter)
	}
	k := imgops.Percentile(ratios, 50)
	if !(k > 0) || math.IsInf(k, 0) {
		return nil, fmt.Sprintf("degenerate %s/%s continuum scale (%.3g)", line, refFilter, k)
	}

	// Raw-layer noise BEFORE the pixels are overwritten — the unit of the faintness metric.
	sigma := noise.Measure(ha).Sigma
	kf := float32(k)
	for i, v := range hp {
		e := v - kf*rp[i]
		if e < 0 {
			e = 0
		}
		hp[i] = e
	}
	p995 := imgops.Percentile(imgops.Subsample(hp, 200_000), 99.5)

	out := filepath.Join(writeDir, outBase+".fits")
	if err := ha.WriteFITS(out); err != nil {
		return nil, "write " + line + " excess layer: " + err.Error()
	}
	hc := &haContinuum{ExcessPath: out, K: k, Ref: refFilter}
	if sigma > 0 {
		hc.P995Sigma = p995 / sigma
		hc.Faint = hc.P995Sigma < haFaintSigma
	}
	return hc, ""
}
