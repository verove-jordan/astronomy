package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Image-orientation hygiene shared by the group merge and the channel combine: the plate-solve parity
// probe, the BAYERPAT strip that keeps Siril from treating mono frames as CFA, and the master-level
// parity normalization that makes a mirrored channel master impossible to combine unnoticed.

// parityProbeName is the throwaway image a parity probe saves next to the probed frame. Probes run
// sequentially per directory, so a fixed name cannot collide.
const parityProbeName = "_parity_probe"

// probeImageParity plate-solves loadName (relative to dir, WITHOUT flipping its pixels) and returns
// the parity sign of the solved WCS: -1 (the East-left target convention) or +1 (mirror-flipped), or
// 0 with a human-readable reason when the solve fails or the WCS lacks a CD/PC matrix.
func probeImageParity(ctx context.Context, runner *siril.Runner, solve siril.SolveOptions, dir, loadName string) (int, string) {
	if _, err := runner.Run(ctx, dir, siril.ParityProbeScript(loadName, parityProbeName, solve), nil); err != nil {
		return 0, "parity undetermined (plate-solve failed) — flip not applied"
	}
	probePath := filepath.Join(dir, parityProbeName+".fits")
	defer func() { _ = os.Remove(probePath) }()
	f, err := fits.Open(probePath)
	if err != nil {
		return 0, "parity undetermined (no solved WCS) — flip not applied"
	}
	det, ok := f.Header.CDDeterminant()
	if !ok {
		return 0, "parity undetermined (WCS lacks CD/PC matrix) — flip not applied"
	}
	if det < 0 {
		return -1, ""
	}
	return 1, ""
}

// stripBayerPattern removes a spurious BAYERPAT card from Siril-written calibrated frames (ASICAP
// stamps one even on mono rigs). Left in place, Siril treats the frames as un-debayered CFA during
// plate-solving and registration — which is exactly where the parity probe reads its sign. The paths
// are Siril-produced copies, never the hardlinked originals, so editing them is safe. Soft-fail: the
// first error is returned as a note ("" when everything went through).
func stripBayerPattern(paths []string) string {
	for _, p := range paths {
		if err := fits.StripKeyword(p, "BAYERPAT"); err != nil {
			return "BAYERPAT strip skipped: " + err.Error()
		}
	}
	return ""
}

// normalizeMasterParity makes every channel master share the reference channel's mirror parity before
// cross-channel registration: each master is plate-solved (-noflip) and any whose det(CD) sign differs
// from the reference's is mirrored in place. Star registration matches asterisms by chirality, so an
// opposite-parity master can never co-register — this is the last-line guard that catches a wrong
// upstream per-group flip as well as a genuinely mirrored foreign session. Soft-fail: masters whose
// parity cannot be determined are left untouched (the register step decides their fate).
func normalizeMasterParity(ctx context.Context, opts Options, masters map[string]string, ordered []string, res *Result) {
	if len(ordered) < 2 {
		return
	}
	signs := make(map[string]int, len(ordered))
	for _, f := range ordered {
		dir, base := masterDirBase(masters[f])
		signs[f], _ = probeImageParity(ctx, opts.Runner, opts.Solve, dir, base)
	}
	ref := ""
	for _, f := range ordered {
		if signs[f] != 0 {
			ref = f
			break
		}
	}
	if ref == "" {
		res.Warnings = append(res.Warnings, "channel parity check skipped (no master could be plate-solved)")
		return
	}
	for _, f := range ordered {
		if signs[f] == 0 || signs[f] == signs[ref] {
			continue
		}
		mirrorMasterInPlace(ctx, opts, masters[f], f, ref, res)
	}
}

// mirrorMasterInPlace flips one channel master about the horizontal axis (parity inversion) and
// refreshes its preview PNG so the on-disk artifacts stay consistent. Failures are warnings only.
func mirrorMasterInPlace(ctx context.Context, opts Options, path, filter, ref string, res *Result) {
	dir, base := masterDirBase(path)
	if _, err := opts.Runner.Run(ctx, dir, siril.MirrorFramesScript([]string{base}), nil); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("channel %s: parity flip failed (%v) — master left mirrored", filter, err))
		return
	}
	res.Warnings = append(res.Warnings,
		fmt.Sprintf("channel %s: master mirror-corrected (opposite parity vs %s) before channel alignment", filter, ref))
	if opts.Preset != nil && opts.Preset.Previews {
		_, _ = opts.Runner.Run(ctx, dir, siril.PreviewScript(base+".fits", base+"_preview", 0.5), nil)
	}
}

// masterDirBase splits a master path into its directory and extension-less basename (Siril load name).
func masterDirBase(path string) (dir, base string) {
	return filepath.Dir(path), strings.TrimSuffix(filepath.Base(path), ".fits")
}
