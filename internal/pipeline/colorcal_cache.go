package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// colorCalCacheMeta is what a cache hit must restore beside the pixels: the human note for
// run.json and the method, whose value gates downstream decisions (SCNR only after SPCC).
type colorCalCacheMeta struct {
	Note   string                `json:"note"`
	Method postprocess.CalMethod `json:"method"`
}

// colorCalibrateCached wraps postprocess.ColorCalibrate with the linear-prep content cache so a
// rerun / Tier-B star-fix pass over a byte-identical calibrated input skips the plate-solve + SPCC
// — including the multi-minute online Gaia fetch when the offline photometric catalogue is absent.
// The key folds in every calibration option. Only a REAL calibration (SPCC or star-field) is
// persisted: the neutralization/network-miss fallback must retry next time, never replay from
// cache. Artifacts: <outDir>/linear/rgb_base_spcc.fits + .sig + .json. Best-effort throughout.
func colorCalibrateCached(ctx context.Context, opts Options, runner *siril.Runner, outDir, base string,
	cc postprocess.ColorCalOptions) (string, postprocess.CalMethod, error) {
	path := filepath.Join(outDir, base+".fits")
	sig, err := fileSHA256(path)
	if err != nil {
		return postprocess.ColorCalibrate(ctx, runner, outDir, base, cc)
	}
	sig += "|" + ccOptionsHash(cc)
	cacheFits := filepath.Join(outDir, linearDirName, "rgb_base_spcc.fits")
	cacheSig, cacheMeta := cacheFits+".sig", cacheFits+".json"
	if fileExists(cacheFits) {
		if b, e := os.ReadFile(cacheSig); e == nil && string(b) == sig {
			var meta colorCalCacheMeta
			if mb, e := os.ReadFile(cacheMeta); e == nil && json.Unmarshal(mb, &meta) == nil && meta.Method.Calibrated() {
				if e := fsutil.CopyFile(cacheFits, path); e == nil {
					n := meta.Note + " (reused from cache — plate-solve + colour calibration skipped)"
					opts.report(Progress{Line: n}) // surface the reuse the moment it happens
					return n, meta.Method, nil
				}
			}
		}
	}
	note, method, err := postprocess.ColorCalibrate(ctx, runner, outDir, base, cc)
	if err == nil && method.Calibrated() {
		if e := fsutil.EnsureDir(filepath.Dir(cacheFits)); e == nil {
			if e := fsutil.CopyFile(path, cacheFits); e == nil {
				_ = os.WriteFile(cacheSig, []byte(sig), 0o644)
				if mb, merr := json.Marshal(colorCalCacheMeta{Note: note, Method: method}); merr == nil {
					_ = os.WriteFile(cacheMeta, mb, 0o644)
				}
			}
		}
	}
	return note, method, err
}

// ccOptionsHash folds every calibration option (SPCC sensor/filters, solve inputs, toggles) into
// the cache key so any changed setting correctly misses.
func ccOptionsHash(cc postprocess.ColorCalOptions) string {
	b, err := json.Marshal(cc)
	if err != nil {
		return "opts-unhashable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
