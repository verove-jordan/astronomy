// Durable per-run checkpoint: the linear finish prep (the Tier-A composite inputs) and a stage
// manifest recording the preset that produced the run's current on-disk artifacts. Together they let
// a later parameter edit re-enter the pipeline at the cheapest stage that reflects the change — the
// same cost-aware tiered re-entry the supervised finish uses (supervise_reentry.go), but durable and
// driven by hand from the processing view. See rerun.go for the re-entry that consumes them.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/mode"
)

const (
	linearDirName     = "linear"      // <outDir>/linear/: the persisted Tier-A composite inputs
	prepFileName      = "prep.json"   // sidecar describing the persisted prep (gimp.Inputs + notes)
	stageManifestName = "stages.json" // <outDir>/stages.json: the preset behind the current artifacts
)

// linearPrep is the persisted Tier-A composite input: the stretched base/lum/ha TIFFs (now living
// under <outDir>/linear/) plus the colour-calibration notes of the prep that produced them.
type linearPrep struct {
	Inputs gimp.Inputs `json:"inputs"`
	Notes  []string    `json:"notes,omitempty"`
}

// persistLinearPrep copies the stretched base/lum/ha TIFFs of a finished linear prep into
// <outDir>/linear/ and writes prep.json describing them, so a later composite-only re-render (e.g. a
// lum_opacity / saturation tweak — Tier A) reuses them in seconds instead of rebuilding the prep.
// Only the structural inputs (Base/Lum/Ha/Color/CalibratedColor) are persisted; the Tier-A knobs are
// re-applied per render from the working preset (buildComposite), so a persisted prep stays valid
// across any composite tweak. Best-effort by contract of its callers: a missing prep just makes a
// Tier-A rerun rebuild from Tier B.
func persistLinearPrep(outDir string, in gimp.Inputs, notes []string) error {
	dir := filepath.Join(outDir, linearDirName)
	if err := fsutil.EnsureDir(dir); err != nil {
		return err
	}
	durable := in
	components := []struct {
		src  string
		dst  *string
		name string
	}{
		{in.Base, &durable.Base, "base.tif"},
		{in.Lum, &durable.Lum, "lum.tif"},
		{in.Ha, &durable.Ha, "ha.tif"},
		{in.OIII, &durable.OIII, "oiii.tif"},
		{in.SII, &durable.SII, "sii.tif"},
	}
	for _, c := range components {
		if c.src == "" {
			continue
		}
		dst := filepath.Join(dir, c.name)
		if err := fsutil.CopyFile(c.src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", c.name, err)
		}
		*c.dst = dst
	}
	return writeJSONAtomic(filepath.Join(dir, prepFileName), linearPrep{Inputs: durable, Notes: notes})
}

// loadLinearPrep reads the persisted Tier-A prep from <outDir>/linear/prep.json. ok is false when no
// usable prep exists (an older run, a failed copy, or a missing base TIFF) — the caller rebuilds it
// from the on-disk channel masters (Tier B), which is always safe.
func loadLinearPrep(outDir string) (gimp.Inputs, []string, bool) {
	b, err := os.ReadFile(filepath.Join(outDir, linearDirName, prepFileName))
	if err != nil {
		return gimp.Inputs{}, nil, false
	}
	var p linearPrep
	if err := json.Unmarshal(b, &p); err != nil {
		return gimp.Inputs{}, nil, false
	}
	if p.Inputs.Base == "" || !fileExists(p.Inputs.Base) {
		return gimp.Inputs{}, nil, false
	}
	return p.Inputs, p.Notes, true
}

// stageManifest records the preset that produced a run's current on-disk artifacts, so a rerun can
// compute tierOf(manifest.Preset, edited) — the cheapest stage that must re-run for a parameter
// change — from the true baseline. Written at the end of every run and refreshed after every rerun.
type stageManifest struct {
	RunID       string      `json:"run_id"`
	OutDir      string      `json:"out_dir"`
	Mode        string      `json:"mode"`
	Preset      mode.Preset `json:"preset"`
	UpdatedAtMs int64       `json:"updated_at_ms"`
}

// writeStageManifest persists the checkpoint manifest for a run (atomic temp+rename). Best-effort at
// its call sites; a missing manifest makes a rerun fall back to the job's stored params as baseline.
func writeStageManifest(outDir string, preset *mode.Preset, runID string) error {
	if preset == nil {
		return fmt.Errorf("stage manifest: nil preset")
	}
	m := stageManifest{
		RunID:       runID,
		OutDir:      outDir,
		Mode:        string(preset.Mode),
		Preset:      *preset,
		UpdatedAtMs: time.Now().UnixMilli(),
	}
	return writeJSONAtomic(filepath.Join(outDir, stageManifestName), m)
}

// readStageManifest loads a run's checkpoint manifest; ok is false when none was written.
func readStageManifest(outDir string) (*stageManifest, bool) {
	b, err := os.ReadFile(filepath.Join(outDir, stageManifestName))
	if err != nil {
		return nil, false
	}
	var m stageManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	return &m, true
}

// writeJSONAtomic marshals v and writes it to path via a temp file + rename, so a reader never sees a
// half-written file and a crash mid-write leaves any previous version intact.
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
