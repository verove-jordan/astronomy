package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// sunseqrefine.go lays a sequence out again from what the run left behind.
//
// The expensive half of a sequence is choosing and stacking its phases; the arrangement is seconds.
// So the run persists each chosen master alongside the orientation it solved, and a re-layout reads
// those back rather than going anywhere near the video. Changing the angle, the spacing or the
// palette is then as cheap as any other refine — and, because it replays the same masters through
// the same finish, it reproduces the original sheet exactly when nothing was changed.

// sequenceDirName is where a run keeps its panels and its record.
const sequenceDirName = "sequence"

// refineSequence re-renders the sheets from a run's persisted panels, falling back to rebuilding
// them from the run's own extracted frames when it has none yet.
//
// The fallback is what makes a run recoverable. A sequence that was never rendered — because the run
// was stopped, or because it predates the feature — still has its frames on disk, and those cost
// hours where everything else costs minutes.
func refineSequence(ctx context.Context, opts Options, runDir string, p solar.Preset, object string,
	say func(string)) ([]string, []string) {

	seqDir := filepath.Join(runDir, sequenceDirName)
	rec, err := readSequenceRecord(seqDir)
	if err != nil {
		say("no panels persisted yet — rebuilding them from the run's own extracted frames")
		return rebuildSequence(ctx, opts, runDir, p, object, say)
	}
	panels, warnings := reloadPanels(rec, p)
	if len(panels) < 3 {
		return nil, append(warnings, "refine sun: phase sequence: fewer than three panels could be reloaded")
	}
	canvas, err := solar.PlanSequenceCanvas(len(panels), medianRadius(panels), p.CropMargin, p.SequenceLayoutOpts())
	if err != nil {
		return nil, append(warnings, "refine sun: phase sequence: "+err.Error())
	}
	outs, rWarn := renderSequenceSheets(panels, canvas, p, runDir, object, say)
	return outs, append(warnings, rWarn...)
}

// reloadPanels reads each persisted master back and re-fits its geometry.
//
// The geometry is re-measured rather than read from the record for the same reason a re-finish
// re-measures the PSF: the master has not changed, so the fit returns exactly what the run got, and
// keeping ONE code path means the two can never drift into two different answers for the same
// pixels. The orientation IS taken from the record — it was solved across the whole set, and a
// single reloaded panel has no way to rediscover the handedness on its own.
func reloadPanels(rec sequenceRecord, p solar.Preset) ([]*seqPanel, []string) {
	var panels []*seqPanel
	var warnings []string
	for _, r := range rec.Panels {
		im, err := fits.ReadImage(r.Master)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("refine sun: phase sequence: panel %d: %v", r.Index, err))
			continue
		}
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		g, ok := solar.FitGeometry(mono, p.TwoBody)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("refine sun: phase sequence: panel %d: no limb in %s",
				r.Index, filepath.Base(r.Master)))
			continue
		}
		panels = append(panels, &seqPanel{
			Master: mono, Pair: g, Source: r.Source, Orient: r.Orientation,
			Frame: solar.PanelFrame{
				Source: r.Source, Sun: g.Sun, Moon: g.Moon, SkyPADeg: r.MoonPADeg,
				ParallacticDeg: r.ParallacticDeg, Flatten: r.RefractFlatten,
			},
		})
	}
	return panels, warnings
}

func readSequenceRecord(seqDir string) (sequenceRecord, error) {
	b, err := os.ReadFile(filepath.Join(seqDir, "sequence.json"))
	if err != nil {
		return sequenceRecord{}, err
	}
	var rec sequenceRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return sequenceRecord{}, err
	}
	if len(rec.Panels) == 0 {
		return sequenceRecord{}, fmt.Errorf("the record lists no panels")
	}
	return rec, nil
}
