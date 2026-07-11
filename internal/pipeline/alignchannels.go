package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Cross-channel master registration: every channel master is co-registered onto one reference grid
// before the colour combine. This file owns the robustness around that step — master parity
// normalization (parity.go), a per-channel pair-register rescue, and the policy deciding what a
// still-failing channel costs (itself, the luminance, or the whole aligned set — never silently).

// alignChannels co-registers the channel masters (filter → absolute master path) and copies each
// registered image to outDir as aligned_<tag>.fits, returning filter → extension-less basename for
// the combine. Failures degrade PER CHANNEL (see applyAlignPolicy); only a failed R/G/B primary
// reverts the whole set to the unaligned masters.
func alignChannels(ctx context.Context, opts Options, masters map[string]string, alignDir, outDir string, res *Result) map[string]string {
	unaligned := map[string]string{}
	for f := range masters {
		unaligned[f] = "master_" + filterTag(f)
	}
	if len(masters) < 2 {
		return unaligned // single channel: nothing to co-register
	}
	ordered := orderedFilters(masters)
	// Opposite-parity masters can never star-register — normalize BEFORE registering (see parity.go).
	normalizeMasterParity(ctx, opts, masters, ordered, res)

	if err := symlinkOrdered(alignDir, ordered, masters); err != nil {
		res.Warnings = append(res.Warnings, "alignment skipped: "+err.Error())
		return unaligned
	}
	if _, err := opts.Runner.Run(ctx, alignDir, siril.AlignMastersScript("ch"), nil); err != nil {
		res.Warnings = append(res.Warnings, "cross-channel alignment failed, using unaligned channels: "+err.Error())
		return unaligned
	}
	aligned, failed := collectAligned(alignDir, outDir, ordered, res)
	failed = retryPairAlign(ctx, opts, masters, aligned, failed, filepath.Dir(alignDir), outDir)
	if len(failed) == 0 {
		return aligned
	}
	return applyAlignPolicy(aligned, unaligned, failed, res)
}

// symlinkOrdered links the masters into dir as 0_<F>.fits, 1_<F>.fits… so Siril's link builds the
// sequence in channel order (ch_00001 = ordered[0], …).
func symlinkOrdered(dir string, ordered []string, masters map[string]string) error {
	if err := fsutil.EnsureDir(dir); err != nil {
		return err
	}
	for i, f := range ordered {
		link := filepath.Join(dir, fmt.Sprintf("%d_%s.fits", i, f))
		_ = removeIfExists(link)
		if err := os.Symlink(masters[f], link); err != nil {
			return err
		}
	}
	return nil
}

// collectAligned copies every channel whose registered frame exists to outDir (aligned_<tag>.fits)
// and returns the channels that did not register (or could not be copied) as failed.
func collectAligned(alignDir, outDir string, ordered []string, res *Result) (map[string]string, []string) {
	aligned := map[string]string{}
	var failed []string
	for i, f := range ordered {
		reg := filepath.Join(alignDir, fmt.Sprintf("r_ch_%05d.fits", i+1))
		if !fileExists(reg) {
			failed = append(failed, f)
			continue
		}
		dst := "aligned_" + filterTag(f)
		if err := fsutil.CopyFile(reg, filepath.Join(outDir, dst+".fits")); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("channel %s: alignment copy failed: %v", f, err))
			failed = append(failed, f)
			continue
		}
		aligned[f] = dst
	}
	return aligned, failed
}

// retryPairAlign re-registers each failed master alone against an already-aligned reference channel
// (a 2-image sequence with the reference pinned via setref) — rescuing channels that lost the JOINT
// registration only to the sequence-wide reference choice or quality spread. Rescued channels are
// added to aligned; the rest are returned.
func retryPairAlign(ctx context.Context, opts Options, masters, aligned map[string]string, failed []string, workDir, outDir string) []string {
	if len(aligned) == 0 || len(failed) == 0 {
		return failed
	}
	refFile := filepath.Join(outDir, aligned[orderedFilters(aligned)[0]]+".fits")
	var still []string
	for _, f := range failed {
		pairDir := filepath.Join(workDir, "pair_"+sanitize(f))
		if pairAlignOne(ctx, opts, refFile, masters[f], f, pairDir, outDir) {
			aligned[f] = "aligned_" + filterTag(f)
		} else {
			still = append(still, f)
		}
	}
	return still
}

// pairAlignOne registers one master against refPath (pinned reference) and copies the result to
// outDir as aligned_<tag>.fits. Returns false on any failure (best-effort rescue).
func pairAlignOne(ctx context.Context, opts Options, refPath, masterPath, filter, pairDir, outDir string) bool {
	if err := fsutil.EnsureDir(pairDir); err != nil {
		return false
	}
	for i, p := range []string{refPath, masterPath} {
		link := filepath.Join(pairDir, fmt.Sprintf("%d_pair.fits", i))
		_ = removeIfExists(link)
		if err := os.Symlink(p, link); err != nil {
			return false
		}
	}
	if _, err := opts.Runner.Run(ctx, pairDir, siril.AlignPairScript("pair"), nil); err != nil {
		return false
	}
	reg := filepath.Join(pairDir, "r_pair_00002.fits")
	if !fileExists(reg) {
		return false
	}
	return fsutil.CopyFile(reg, filepath.Join(outDir, "aligned_"+filterTag(filter)+".fits")) == nil
}

// applyAlignPolicy decides what each still-unregistered channel costs. A failed R/G/B primary makes a
// true-colour combine impossible → the whole set reverts to the unaligned masters (the pre-existing
// behaviour, now the last resort). A failed L is dropped (RGB-only composite). A failed accent channel
// (Ha/OIII/SII/…) is dropped: screened at the wrong position it would paint signal where none belongs,
// strictly worse than omitting it. Every decision is a run warning.
func applyAlignPolicy(aligned, unaligned map[string]string, failed []string, res *Result) map[string]string {
	for _, f := range failed {
		switch f {
		case "R", "G", "B":
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("channel %s could not be co-registered — combining unaligned channels", f))
			return unaligned
		case "L":
			res.Warnings = append(res.Warnings, "L could not be co-registered — combining without luminance")
		default:
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s could not be co-registered — omitted from the composite", f))
		}
	}
	return aligned
}
