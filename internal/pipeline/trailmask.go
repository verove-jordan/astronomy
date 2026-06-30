package pipeline

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/transient"
)

// maskChannelTrails removes cross-frame transients (satellite/plane trail segments, cosmic rays, hot
// pixels) from a channel's REGISTERED sub-frames before they are stacked. It loads every
// <regSeq>_NNNNN.fits in seqDir, runs transient.MaskCrossFrame (per-pixel median-outlier replacement),
// and writes the cleaned pixels back in place (preserving each frame's header). This catches a slow
// satellite that lands in many subs at marching positions — which per-frame trail rejection cannot drop
// without losing the whole channel, and a normal stack sigma-clip is too loose to remove — at no global
// SNR cost (only outlier pixels are touched).
//
// All registered frames are masked, including any later graded out of the stack: masking them is
// harmless and a larger sample makes the per-pixel median more robust. Returns a human-readable note
// (empty when it did nothing). Memory: holds the channel's registered frames at once (~64 MB each);
// channels are processed sequentially so only one channel is resident.
func maskChannelTrails(seqDir, regSeq string, k float64) (string, error) {
	if k <= 0 {
		return "", nil
	}
	paths, err := filepath.Glob(filepath.Join(seqDir, regSeq+"_*.fits"))
	if err != nil {
		return "", err
	}
	if len(paths) < transient.MinFrames {
		return "", nil // too few frames for a robust per-pixel statistic
	}
	sort.Strings(paths)

	frames := make([]*fits.Image, len(paths))
	for i, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", filepath.Base(p), err)
		}
		frames[i] = im
	}
	masked, err := transient.MaskCrossFrame(frames, k)
	if err != nil {
		return "", err
	}
	if masked == 0 {
		return "", nil
	}
	for i, p := range paths {
		if err := frames[i].OverwriteData(p); err != nil {
			return "", fmt.Errorf("write %s: %w", filepath.Base(p), err)
		}
	}
	pct := 100 * float64(masked) / float64(len(frames)*frames[0].W*frames[0].H)
	return fmt.Sprintf("cross-frame trail mask: cleaned %d transient pixels (%.2f%%) across %d registered frames",
		masked, pct, len(frames)), nil
}
