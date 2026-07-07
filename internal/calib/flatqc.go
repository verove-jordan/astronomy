package calib

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/optics"
)

// analyzeFlatMaster runs optical-defect + quality analysis on a freshly-built master flat: it detects
// dust donuts / blotches and checks the flat's level, saturation and vignetting, writes a JSON + PNG
// sidecar next to the master, and returns human-readable warnings for the run log. Soft-fail: any
// analysis error yields a single note and never blocks the build. rawFlats are the source frames (used
// for the saturation check, which the stacked master hides).
func analyzeFlatMaster(masterPath string, rawFlats []string) []string {
	qc, defects, err := optics.AnalyzeFlat(masterPath, rawFlats)
	if err != nil {
		return []string{fmt.Sprintf("flat QC skipped for %s: %v", filepath.Base(masterPath), err)}
	}
	// The overlay artifacts are a convenience; a failure to write them must not drop the QC warnings.
	_ = optics.WriteArtifacts(masterPath, qc, defects)
	return flatQCWarnings(masterPath, qc, defects)
}

// flatQCWarnings renders the QC status and detected defects as concise run-log warnings.
func flatQCWarnings(masterPath string, qc optics.FlatQC, defects []optics.Defect) []string {
	base := filepath.Base(masterPath)
	var out []string
	if (qc.Status == "warn" || qc.Status == "bad") && len(qc.Notes) > 0 {
		out = append(out, fmt.Sprintf("flat %s (%s): %s", base, qc.Status, strings.Join(qc.Notes, "; ")))
	}
	if len(defects) > 0 {
		worst := worstDefect(defects)
		kind := "blotch"
		if worst.Donut {
			kind = "donut"
		}
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		out = append(out, fmt.Sprintf("flat %s: %d optical defect(s) detected (worst %.0f%% %s) — see %s_defects.png",
			base, len(defects), worst.Depth*100, kind, stem))
	}
	return out
}

// worstDefect returns the deepest (most attenuating) defect.
func worstDefect(defects []optics.Defect) optics.Defect {
	worst := defects[0]
	for _, d := range defects[1:] {
		if d.Depth > worst.Depth {
			worst = d
		}
	}
	return worst
}
