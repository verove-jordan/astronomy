package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/noise"
)

// Hot/cold pixels that were never dark-calibrated survive the stack as LONE one-pixel spikes and
// holes (the "black pepper" across a bright galaxy body): rejection can only catch them when
// dithering moved them between frames, and the drift-only captures of the legacy nights barely
// dither. A stacked master's real detail is never a single isolated pixel — stars carry PSF wings —
// so a conservative isolated-outlier median repair is safe there and runs BEFORE any background
// model or denoise can smear the spikes into their surroundings.
const (
	// masterCosmeticK: a pixel must sit this many master-noise sigmas away from its 8-neighbour
	// median to be a candidate — far above any real single-pixel detail on a stacked, slightly
	// dithered master.
	masterCosmeticK = 8.0
	// masterCosmeticIsolation: the strongest NEIGHBOUR deviation must stay below this fraction of
	// the candidate's own deviation — a real star core has bright wings next to it, a lone hot/cold
	// pixel does not.
	masterCosmeticIsolation = 0.35
)

// repairIsolatedOutliers replaces lone hot/cold pixels with their 8-neighbour median, returning how
// many pixels were repaired. Border pixels are left untouched.
func repairIsolatedOutliers(im *fits.Image, sigma float64) int {
	if sigma <= 0 || im == nil || len(im.Pix) == 0 || im.W < 3 || im.H < 3 {
		return 0
	}
	repaired := 0
	thr := float32(masterCosmeticK * sigma)
	for c := range im.Pix {
		p := im.Pix[c]
		src := make([]float32, len(p))
		copy(src, p)
		nb := make([]float32, 0, 8)
		for y := 1; y < im.H-1; y++ {
			for x := 1; x < im.W-1; x++ {
				i := y*im.W + x
				v := src[i]
				nb = nb[:0]
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						nb = append(nb, src[i+dy*im.W+dx])
					}
				}
				sort.Slice(nb, func(a, b int) bool { return nb[a] < nb[b] })
				med := (nb[3] + nb[4]) / 2
				dev := v - med
				if dev < 0 {
					dev = -dev
				}
				if dev <= thr {
					continue
				}
				// Isolation: the most deviant neighbour must be far tamer than the candidate,
				// else this is the flank of real structure (star core/wing), not a lone defect.
				maxNb := float32(0)
				for _, n := range nb {
					d := n - med
					if d < 0 {
						d = -d
					}
					if d > maxNb {
						maxNb = d
					}
				}
				if maxNb > float32(masterCosmeticIsolation)*dev {
					continue
				}
				p[i] = med
				repaired++
			}
		}
	}
	return repaired
}

// masterCosmeticNote marks the channels this repair applies to: only masters whose frames never saw
// a dark (nothing else repaired their fixed-pattern defects). Runs on the stacked master in place.
func masterCosmetic(ch *ChannelResult, masterPath string) {
	darkless := false
	for _, n := range ch.Selection.Notes {
		if strings.Contains(n, "no matching dark") || strings.Contains(n, "no darks available") {
			darkless = true
			break
		}
	}
	if !darkless {
		return
	}
	im, err := fits.ReadImage(masterPath)
	if err != nil {
		return
	}
	sigma := noise.Measure(im).Sigma
	n := repairIsolatedOutliers(im, sigma)
	if n == 0 {
		return
	}
	if err := im.WriteFITS(masterPath); err != nil {
		ch.Selection.Notes = append(ch.Selection.Notes, "cosmetic repair not written: "+err.Error())
		return
	}
	ch.Selection.Notes = append(ch.Selection.Notes,
		fmt.Sprintf("cosmetic: %d isolated hot/cold pixel(s) median-repaired on the master (no dark calibration)", n))
}
