package annotate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/skycat"
)

const (
	// linearDirName mirrors the pipeline's checkpoint dir — the annotation scratch lives beside
	// the persisted linear masters.
	linearDirName = "linear"
	scratchBase   = "annotate_wcs"
	solveTimeout  = 60 * time.Second
)

// Solve reason vocabulary (Solve.Reason when Solved=false).
const (
	reasonNoWCS            = "no_wcs"            // no stored solution and no solver available
	reasonNoCoordsHint     = "no_coords_hint"    // nothing to seed a solve with (phone/milkyway path)
	reasonSolveFailed      = "solve_failed"      // Siril ran but produced no usable solution
	reasonValidationFailed = "validation_failed" // projection did not line up with detected stars
)

// findWCS returns a TAN solution for the master's pixel grid: the master's own header first, then
// the cached scratch solve, then one fresh Siril solve (never blind — a coords hint is required).
// method is "" when unsolved, with reason set.
func findWCS(ctx context.Context, o Options, in inputs, masterHdr *fits.Header) (w fits.WCS, method, reason string) {
	if wcs, ok := fits.ParseWCS(masterHdr); ok {
		return wcs, "pipeline", ""
	}

	scratch := filepath.Join(o.RunDir, linearDirName, scratchBase+".fits")
	sigPath := scratch + ".sig"
	sig, sigErr := fileSHA256(in.masterAbs)
	if sigErr == nil {
		if wcs, ok := cachedSolve(scratch, sigPath, sig); ok {
			return wcs, "cached", ""
		}
	}

	if o.Runner == nil {
		return fits.WCS{}, "", reasonNoWCS
	}
	coords, ok := coordsHint(masterHdr, in.info.Object, o.CatalogDir)
	if !ok {
		return fits.WCS{}, "", reasonNoCoordsHint
	}
	if err := fsutil.EnsureDir(filepath.Dir(scratch)); err != nil {
		return fits.WCS{}, "", reasonSolveFailed
	}
	if err := fsutil.CopyFile(in.masterAbs, scratch); err != nil {
		return fits.WCS{}, "", reasonSolveFailed
	}

	solve := o.Solve
	solve.Coords = coords
	if v, ok := masterHdr.Float("FOCALLEN"); ok && v > 0 {
		solve.FocalMM = v
	}
	if v, ok := masterHdr.Float("XPIXSZ"); ok && v > 0 {
		solve.PixelUm = v
	}

	sctx, cancel := context.WithTimeout(ctx, solveTimeout)
	defer cancel()
	script := siril.ParityProbeScript(scratchBase, scratchBase, solve)
	if _, err := o.Runner.Run(sctx, filepath.Dir(scratch), script, nil); err != nil {
		return fits.WCS{}, "", reasonSolveFailed
	}
	f, err := fits.Open(scratch)
	if err != nil {
		return fits.WCS{}, "", reasonSolveFailed
	}
	wcs, ok := fits.ParseWCS(f.Header)
	if !ok {
		return fits.WCS{}, "", reasonSolveFailed
	}
	if sigErr == nil {
		_ = os.WriteFile(sigPath, []byte(sig), 0o644)
	}
	return wcs, "resolved", ""
}

// cachedSolve returns the scratch solve's WCS when its signature still matches the current master
// (a re-run rewrites the master and correctly misses).
func cachedSolve(scratch, sigPath, sig string) (fits.WCS, bool) {
	b, err := os.ReadFile(sigPath)
	if err != nil || string(b) != sig {
		return fits.WCS{}, false
	}
	f, err := fits.Open(scratch)
	if err != nil {
		return fits.WCS{}, false
	}
	return fits.ParseWCS(f.Header)
}

// coordsHint seeds the solve: the master's own OBJCTRA/OBJCTDEC header first, then the run's
// object name resolved through the DSO catalogue. No hint → no solve (never blind).
func coordsHint(h *fits.Header, object, catalogDir string) (string, bool) {
	raStr, _ := h.String("OBJCTRA")
	decStr, _ := h.String("OBJCTDEC")
	if ra, ok1 := skycat.ParseRA(raStr); ok1 {
		if dec, ok2 := skycat.ParseDec(decStr); ok2 {
			return fmt.Sprintf("%.5f,%.5f", ra, dec), true
		}
	}
	if object != "" {
		if ra, dec, ok := skycat.ResolveCoords(object, catalogDir); ok {
			return fmt.Sprintf("%.5f,%.5f", ra, dec), true
		}
	}
	return "", false
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
