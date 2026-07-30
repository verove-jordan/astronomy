// Iteration memory for the supervised finish. The loop records every pass's params/scores/defects
// and feeds a COMPACT rolling history into each next critique — the model sees what it already
// tried, what improved and what worsened, so it walks a gradient instead of re-rolling dice. The
// same records, persisted per job, warm-start the NEXT run/refine of the same target (cross-run
// memory): the working preset seeds from the best prior pass and the model is told its score.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

const (
	historyMaxEntries = 6    // last N passes shown to the model
	historyMaxChars   = 1400 // hard cap on the block (prompt budget)
	warmStartMinDet   = 6.0  // prior passes below this deterministic score never seed a run
	presetBlobVersion = 1    // forward-compat marker on persisted preset blobs
)

// iterOutcome is one completed pass, kept in-loop for the history block.
type iterOutcome struct {
	index    int
	tier     string
	params   map[string]any
	det      float64
	model    float64
	combined float64
	detail   float64 // DetailIndex of the pass (relative-acutance baseline for later passes)
	defects  []postprocess.Defect
	note     string // one-line assessment (trimmed reasoning)
}

// historyBlock renders the last passes as a compact, oldest-first digest for the critique prompt:
// per pass the params DIFF vs its predecessor (the local gradient — the current full state is
// already in the prompt), the scores, the top defects, and whether it improved on the best so far.
func historyBlock(outs []iterOutcome, bestIdx int) string {
	if len(outs) == 0 {
		return ""
	}
	start := 0
	if len(outs) > historyMaxEntries {
		start = len(outs) - historyMaxEntries
	}
	var b strings.Builder
	var prev map[string]any
	if start > 0 {
		prev = outs[start-1].params
	}
	for i := start; i < len(outs); i++ {
		o := outs[i]
		diff := diffParams(prev, o.params)
		prev = o.params
		mark := ""
		if i == bestIdx {
			mark = " ← BEST so far"
		}
		fmt.Fprintf(&b, "pass %d [%s] score %.1f (metrics %.1f, model %.1f)%s", o.index+1, o.tier, o.combined, o.det, o.model, mark)
		if diff != "" {
			fmt.Fprintf(&b, " | changed: %s", diff)
		}
		if len(o.defects) > 0 {
			kinds := make([]string, 0, 3)
			for _, d := range o.defects {
				kinds = append(kinds, d.Kind)
				if len(kinds) == 3 {
					break
				}
			}
			fmt.Fprintf(&b, " | defects: %s", strings.Join(kinds, ","))
		}
		if o.note != "" {
			fmt.Fprintf(&b, " | %s", clipStr(o.note, 90))
		}
		b.WriteByte('\n')
	}
	s := strings.TrimRight(b.String(), "\n")
	if len(s) > historyMaxChars {
		s = s[len(s)-historyMaxChars:]
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:] // never start mid-line
		}
	}
	return s
}

// diffParams renders the knobs that changed between two param maps as "k: a→b" pairs (sorted).
func diffParams(prev, cur map[string]any) string {
	if prev == nil {
		return ""
	}
	var parts []string
	for k, v := range cur {
		if fmt.Sprint(prev[k]) != fmt.Sprint(v) {
			parts = append(parts, fmt.Sprintf("%s: %v→%v", k, prev[k], v))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func clipStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// presetBlob serializes the full working preset (versioned) for the finish_iterations memory.
func presetBlob(p mode.Preset) []byte {
	raw, err := json.Marshal(struct {
		V      int         `json:"v"`
		Preset mode.Preset `json:"preset"`
	}{presetBlobVersion, p})
	if err != nil {
		return nil
	}
	return raw
}

// decodePresetBlob restores a persisted working preset; ok=false for foreign/legacy blobs.
func decodePresetBlob(raw []byte) (mode.Preset, bool) {
	var box struct {
		V      int         `json:"v"`
		Preset mode.Preset `json:"preset"`
	}
	if err := json.Unmarshal(raw, &box); err != nil || box.V != presetBlobVersion {
		return mode.Preset{}, false
	}
	return box.Preset, true
}

// warmStart seeds the working preset from the best prior pass of the SAME target across jobs, and
// returns a history preamble telling the model where that seed scored. Poison-proof by design: only
// decent priors qualify (det ≥ warmStartMinDet, enforced by the store query), the seed re-applies
// through the mode's OWN clamp path (a stale/out-of-range blob is bounded), and only the TUNABLE
// fields move (mode identity, IO paths and structural flags stay the caller's).
func warmStart(ctx context.Context, opts Options, working *mode.Preset) string {
	if opts.FinishPriors == nil || opts.PriorObject == "" || working == nil {
		return ""
	}
	priors, err := opts.FinishPriors.BestFinishIterations(ctx, opts.PriorObject, string(working.Mode), warmStartMinDet, 1)
	if err != nil || len(priors) == 0 {
		return ""
	}
	prior := priors[0]
	seed, ok := decodePresetBlob(prior.Preset)
	if !ok {
		return ""
	}
	// Re-apply the prior's tunable surface through the shared param brain: marshal its params and
	// patch them onto the CURRENT working preset, so clamps run and non-tunable fields never move.
	rawParams, err := json.Marshal(tunableJSON(seed))
	if err != nil {
		return ""
	}
	if _, err := ApplyParamPatch(working, rawParams); err != nil {
		return ""
	}
	return fmt.Sprintf("warm start: seeded from the best prior pass of this target (job %d, tier %s, score %.1f — %s)",
		prior.JobID, prior.Tier, prior.Combined, clipStr(prior.Reasoning, 80))
}

// tunableJSON maps a preset's tunable surface into the mode's patch-key vocabulary, so a warm-start
// seed rides the exact same validation path as a model patch.
func tunableJSON(p mode.Preset) map[string]any {
	out := map[string]any{}
	known := knownParamKeys(p.Mode)
	consent := consentParamKeys(p.Mode)
	for k, v := range ParamsFor(p) {
		if known[k] && !consent[k] {
			out[k] = v
		}
	}
	return out
}
