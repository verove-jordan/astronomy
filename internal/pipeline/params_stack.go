// The stacking half of the param brain: the tunable knobs behind the launch form's "Stacking &
// rejection" panel. They are Tier C by definition — changing how pixels are combined can only take
// effect by re-stacking — and they apply to the SIRIL-BACKED modes (deepsky/nebula/livestack/mosaic/
// comet). Planetary and sun stack natively with their own lucky-imaging knobs (best_percent /
// keep_percent / clip_sigma), and milkyway composites through the nightscape recipe, so none of these
// keys is offered there.
//
// Deliberately absent from the supervisor's knob menu (like the mono side-output toggles): re-stacking
// is the most expensive thing the engine does and which algorithm produced a master is a provenance
// fact the user owns, not something a vision model should swap between iterations.
package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// stackPatch is the wire shape of the stacking knobs. It is applied alongside each mode's own patch
// struct (exactly as mosaicPatch is), and knownParamKeys reflects its json tags — so the validated
// key set can never drift from what applyStackPatch actually reads.
type stackPatch struct {
	Engine   *string  `json:"stack_engine,omitempty"`
	Combine  *string  `json:"stack_combine,omitempty"`
	Reject   *string  `json:"stack_reject,omitempty"`
	Low      *float64 `json:"stack_reject_low,omitempty"`
	High     *float64 `json:"stack_reject_high,omitempty"`
	TrimFrac *float64 `json:"stack_trim_frac,omitempty"`
	Norm     *string  `json:"stack_norm,omitempty"`
	FastNorm *bool    `json:"stack_fast_norm,omitempty"`
	Weight   *string  `json:"stack_weight,omitempty"`
	RejMaps  *bool    `json:"stack_rejection_maps,omitempty"`
	Feather  *int     `json:"stack_feather,omitempty"`

	LocalNorm       *bool `json:"stack_local_norm,omitempty"`
	LocalNormDegree *int  `json:"stack_local_norm_degree,omitempty"`

	// The comet-aligned half of a comet run keeps its own rejection: the marching star trails are
	// one-frame HIGH outliers while the coma is constant, so its sigmas are asymmetric by design.
	CometReject *string  `json:"comet_stack_reject,omitempty"`
	CometLow    *float64 `json:"comet_stack_low,omitempty"`
	CometHigh   *float64 `json:"comet_stack_high,omitempty"`

	// Per-frame-type calibration-master recipes. Each type is stacked separately and their pools
	// differ by an order of magnitude — 200 bias frames want GESD where 5 flats want percentile —
	// so each carries its own algorithm. The NORMALIZATION is not exposed: it is fixed by physics
	// (bias/dark un-normalized, flats multiplicative).
	BiasCombine *string  `json:"master_bias_combine,omitempty"`
	BiasReject  *string  `json:"master_bias_reject,omitempty"`
	BiasLow     *float64 `json:"master_bias_low,omitempty"`
	BiasHigh    *float64 `json:"master_bias_high,omitempty"`

	DarkCombine *string  `json:"master_dark_combine,omitempty"`
	DarkReject  *string  `json:"master_dark_reject,omitempty"`
	DarkLow     *float64 `json:"master_dark_low,omitempty"`
	DarkHigh    *float64 `json:"master_dark_high,omitempty"`

	FlatCombine *string  `json:"master_flat_combine,omitempty"`
	FlatReject  *string  `json:"master_flat_reject,omitempty"`
	FlatLow     *float64 `json:"master_flat_low,omitempty"`
	FlatHigh    *float64 `json:"master_flat_high,omitempty"`

	DarkFlatCombine *string  `json:"master_dark_flat_combine,omitempty"`
	DarkFlatReject  *string  `json:"master_dark_flat_reject,omitempty"`
	DarkFlatLow     *float64 `json:"master_dark_flat_low,omitempty"`
	DarkFlatHigh    *float64 `json:"master_dark_flat_high,omitempty"`
}

// masterPatch is one frame type's slice of the stacking patch.
type masterPatch struct {
	combine *string
	reject  *string
	low     *float64
	high    *float64
}

// masterPatches pairs each frame type's knobs with the recipe they edit.
func (p stackPatch) masterPatches(m *stackalg.MasterOptions) []struct {
	patch masterPatch
	dst   *stackalg.Options
} {
	return []struct {
		patch masterPatch
		dst   *stackalg.Options
	}{
		{masterPatch{p.BiasCombine, p.BiasReject, p.BiasLow, p.BiasHigh}, &m.Bias},
		{masterPatch{p.DarkCombine, p.DarkReject, p.DarkLow, p.DarkHigh}, &m.Dark},
		{masterPatch{p.FlatCombine, p.FlatReject, p.FlatLow, p.FlatHigh}, &m.Flat},
		{masterPatch{p.DarkFlatCombine, p.DarkFlatReject, p.DarkFlatLow, p.DarkFlatHigh}, &m.DarkFlat},
	}
}

// applyMaster overlays one frame type's knobs onto its recipe, clamped.
func applyMaster(dst *stackalg.Options, p masterPatch) {
	o := *dst
	if p.combine != nil {
		if c := stackalg.Combine(norm(*p.combine)); combineKnown(c) {
			o.Combine = c
		}
	}
	if p.reject != nil {
		if r, ok := parseReject(*p.reject); ok {
			o.Reject = r
			o.Low, o.High = 0, 0 // a new algorithm starts from ITS defaults
		}
	}
	setF(&o.Low, p.low)
	setF(&o.High, p.high)
	*dst = stackalg.Clamp(o)
}

// applyStackPatch overlays the stacking knobs onto a preset, clamped. Unknown enum VALUES are left
// out (the API's Validate call reports them as an error rather than silently substituting a different
// algorithm — a master must never claim an algorithm that did not produce it).
func applyStackPatch(next mode.Preset, p stackPatch) mode.Preset {
	s := next.Stack
	if p.Engine != nil {
		if e := stackalg.Engine(norm(*p.Engine)); isEngine(e) {
			s.Engine = engineValue(e)
		}
	}
	if p.Combine != nil {
		if c := stackalg.Combine(norm(*p.Combine)); combineKnown(c) {
			s.Combine = c
		}
	}
	if p.Reject != nil {
		if r, ok := parseReject(*p.Reject); ok {
			s.Reject = r
			s.Low, s.High = 0, 0 // a new algorithm starts from ITS defaults, not the old one's sigmas
		}
	}
	setF(&s.Low, p.Low)
	setF(&s.High, p.High)
	setF(&s.TrimFrac, p.TrimFrac)
	if p.Norm != nil {
		if n := stackalg.Norm(norm(*p.Norm)); stackalg.IsNorm(n) {
			s.Norm = n
		}
	}
	setB(&s.FastNorm, p.FastNorm)
	setB(&s.RejMaps, p.RejMaps)
	setI(&s.Feather, p.Feather)
	setB(&s.LocalNorm, p.LocalNorm)
	if p.LocalNormDegree != nil {
		s.LocalNormDegree = clampi(*p.LocalNormDegree, 0, 4) // 0 = the engine default (degree 1)
	}
	next.Stack = stackalg.Clamp(s)

	if p.Weight != nil {
		if w := stackalg.Weight(norm(*p.Weight)); w == "none" || stackalg.IsWeight(w) {
			next.StackWeight = string(weightValue(w))
		}
	}

	masters := next.Masters
	for _, mp := range p.masterPatches(&masters) {
		applyMaster(mp.dst, mp.patch)
	}
	next.Masters = masters

	c := next.StackComet
	if p.CometReject != nil {
		if r, ok := parseReject(*p.CometReject); ok {
			c.Reject = r
			c.Low, c.High = 0, 0
		}
	}
	setF(&c.Low, p.CometLow)
	setF(&c.High, p.CometHigh)
	next.StackComet = stackalg.Clamp(c)
	return next
}

// stackParams is the stacking knobs' contribution to ParamsFor. Enum values are reported in their
// DISPLAY form ("auto"/"none") rather than as the empty string, so the launch form's JSON prefill and
// glossary show a word the user can recognise and re-type.
func stackParams(p mode.Preset) map[string]any {
	return map[string]any{
		"stack_engine":            displayEngine(p.Stack.Engine),
		"stack_combine":           displayCombine(p.Stack.Combine),
		"stack_reject":            displayReject(p.Stack.Reject),
		"stack_reject_low":        p.Stack.Low,
		"stack_reject_high":       p.Stack.High,
		"stack_trim_frac":         p.Stack.TrimFrac,
		"stack_norm":              displayNorm(p.Stack.Norm),
		"stack_fast_norm":         p.Stack.FastNorm,
		"stack_weight":            displayWeight(stackalg.Weight(p.StackWeight)),
		"stack_rejection_maps":    p.Stack.RejMaps,
		"stack_feather":           p.Stack.Feather,
		"stack_local_norm":        p.Stack.LocalNorm,
		"stack_local_norm_degree": p.Stack.LocalNormDegree,
	}
}

// masterStackParams is the per-frame-type calibration recipes' contribution to ParamsFor.
func masterStackParams(p mode.Preset) map[string]any {
	out := map[string]any{}
	for prefix, o := range map[string]stackalg.Options{
		"master_bias":      p.Masters.Bias,
		"master_dark":      p.Masters.Dark,
		"master_flat":      p.Masters.Flat,
		"master_dark_flat": p.Masters.DarkFlat,
	} {
		out[prefix+"_combine"] = displayCombine(o.Combine)
		out[prefix+"_reject"] = displayReject(o.Reject)
		out[prefix+"_low"] = o.Low
		out[prefix+"_high"] = o.High
	}
	return out
}

// masterStackKnobRanges is the numeric bounds for those recipes' two parameters.
func masterStackKnobRanges() map[string]KnobRange {
	out := map[string]KnobRange{}
	for _, prefix := range []string{"master_bias", "master_dark", "master_flat", "master_dark_flat"} {
		out[prefix+"_low"] = KnobRange{Min: 0, Max: stackalg.SigmaMax}
		out[prefix+"_high"] = KnobRange{Min: 0, Max: stackalg.SigmaMax}
	}
	return out
}

// cometStackParams adds the comet-aligned stack's own rejection (comet mode only).
func cometStackParams(p mode.Preset) map[string]any {
	return map[string]any{
		"comet_stack_reject": displayReject(p.StackComet.Reject),
		"comet_stack_low":    p.StackComet.Low,
		"comet_stack_high":   p.StackComet.High,
	}
}

// stackKnobRanges is the stacking knobs' contribution to KnobRangesFor. Only the numeric knobs
// appear — the enums (engine/combine/reject/norm/weight) carry their per-algorithm bounds in
// stackalg.Catalogue instead, which is what the panel renders. The rejection parameters advertise the
// UNION range: their usable span depends on the chosen algorithm and is applied at render time
// (stackalg.Resolve), so the stored value stays exactly what the user asked for.
func stackKnobRanges() map[string]KnobRange {
	return map[string]KnobRange{
		"stack_reject_low":        {Min: 0, Max: stackalg.SigmaMax},
		"stack_reject_high":       {Min: 0, Max: stackalg.SigmaMax},
		"stack_trim_frac":         {Min: 0, Max: 0.45},
		"stack_feather":           {Min: 0, Max: 512, Int: true},
		"stack_local_norm_degree": {Min: 0, Max: 4, Int: true}, // 0 = the engine default (degree 1)
	}
}

// cometStackKnobRanges is the comet-aligned stack's numeric bounds.
func cometStackKnobRanges() map[string]KnobRange {
	return map[string]KnobRange{
		"comet_stack_low":  {Min: 0, Max: stackalg.SigmaMax},
		"comet_stack_high": {Min: 0, Max: stackalg.SigmaMax},
	}
}

// stackChanged reports whether any stacking choice differs — every one of them forces a Tier-C
// re-stack, since no cheaper re-entry re-combines pixels.
func stackChanged(prev, next mode.Preset) bool {
	return prev.Stack != next.Stack ||
		prev.StackComet != next.StackComet ||
		prev.Masters != next.Masters ||
		prev.StackWeight != next.StackWeight
}

// norm lowercases and trims a wire enum value so "Winsorized " and "winsorized" mean the same thing.
func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// parseReject accepts the display word "auto" as well as every catalogue id.
func parseReject(s string) (stackalg.Reject, bool) {
	v := norm(s)
	if v == "auto" || v == "" {
		return stackalg.RejectAuto, true
	}
	r := stackalg.Reject(v)
	if _, ok := stackalg.RejectOf(r); ok {
		return r, true
	}
	return "", false
}

func isEngine(e stackalg.Engine) bool {
	switch e {
	case "", stackalg.EngineAuto, stackalg.EngineSiril, stackalg.EngineNative:
		return true
	}
	return false
}

func combineKnown(c stackalg.Combine) bool {
	_, ok := stackalg.CombineOf(c)
	return ok
}

// engineValue maps the display word "auto" back to the canonical empty value, so re-sending the
// prefilled form is a no-op instead of a Tier-C re-stack.
func engineValue(e stackalg.Engine) stackalg.Engine {
	if e == stackalg.EngineAuto {
		return ""
	}
	return e
}

// weightValue maps the display word "none" back to the empty (unweighted) value.
func weightValue(w stackalg.Weight) stackalg.Weight {
	if w == "none" {
		return stackalg.WeightNone
	}
	return w
}

func displayEngine(e stackalg.Engine) string {
	if e == "" {
		return string(stackalg.EngineAuto)
	}
	return string(e)
}

func displayCombine(c stackalg.Combine) string {
	if c == stackalg.CombineAuto {
		return string(stackalg.CombineMean)
	}
	return string(c)
}

func displayReject(r stackalg.Reject) string {
	if r == stackalg.RejectAuto {
		return "auto"
	}
	return string(r)
}

func displayNorm(n stackalg.Norm) string {
	if n == stackalg.NormAuto {
		return string(stackalg.NormAddScale)
	}
	return string(n)
}

func displayWeight(w stackalg.Weight) string {
	if w == stackalg.WeightNone {
		return "none"
	}
	return string(w)
}

// applyStackPatchRaw decodes the stacking keys out of a raw knob patch and overlays them. A body
// that is not a stacking patch at all (wrong value types) leaves the preset untouched rather than
// failing the whole run — the caller's own patch struct has already reported what it could not read.
func applyStackPatchRaw(next mode.Preset, raw json.RawMessage) mode.Preset {
	var p stackPatch
	if err := json.Unmarshal(raw, &p); err != nil {
		return next
	}
	return applyStackPatch(next, p)
}

// modeHasStackKnobs reports whether a mode's stack is the Siril one the panel configures. Planetary
// and sun combine natively with their own lucky-imaging knobs; milkyway composites through the
// nightscape recipe.
func modeHasStackKnobs(m mode.Mode) bool {
	switch m {
	case mode.Planetary, mode.Sun, mode.Milkyway:
		return false
	}
	return true
}

// validateStackPatch reports an unusable stacking key in a raw knob patch. An unknown KEY is merely
// ignored (ApplyParamPatch reports it back), but a wrong TYPE or an unknown enum VALUE must fail
// loudly: silently substituting a different algorithm would make the run's own provenance a lie —
// run.json would name one algorithm while another produced the master.
func validateStackPatch(m mode.Mode, raw json.RawMessage) error {
	if !modeHasStackKnobs(m) {
		return nil // the keys are not part of this mode's surface; they are reported as ignored
	}
	var p stackPatch
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("stacking knobs: %w", err)
	}
	if p.Engine != nil && !isEngine(stackalg.Engine(norm(*p.Engine))) {
		return fmt.Errorf("unknown stack_engine %q (want auto, siril or native)", *p.Engine)
	}
	if p.Combine != nil && !combineKnown(stackalg.Combine(norm(*p.Combine))) {
		return fmt.Errorf("unknown stack_combine %q (want one of %s)", *p.Combine, combineNames())
	}
	if p.Norm != nil && !stackalg.IsNorm(stackalg.Norm(norm(*p.Norm))) {
		return fmt.Errorf("unknown stack_norm %q (want none, add, addscale, mul or mulscale)", *p.Norm)
	}
	if p.Weight != nil {
		if w := stackalg.Weight(norm(*p.Weight)); w != "none" && !stackalg.IsWeight(w) {
			return fmt.Errorf("unknown stack_weight %q (want none, noise, wfwhm, nbstars or nbstack)", *p.Weight)
		}
	}
	for key, v := range map[string]*string{
		"master_bias_combine": p.BiasCombine, "master_dark_combine": p.DarkCombine,
		"master_flat_combine": p.FlatCombine, "master_dark_flat_combine": p.DarkFlatCombine,
	} {
		if v != nil && !combineKnown(stackalg.Combine(norm(*v))) {
			return fmt.Errorf("unknown %s %q (want one of %s)", key, *v, combineNames())
		}
	}
	for key, v := range map[string]*string{
		"stack_reject": p.Reject, "comet_stack_reject": p.CometReject,
		"master_bias_reject": p.BiasReject, "master_dark_reject": p.DarkReject,
		"master_flat_reject": p.FlatReject, "master_dark_flat_reject": p.DarkFlatReject,
	} {
		if v == nil {
			continue
		}
		if _, ok := parseReject(*v); !ok {
			return fmt.Errorf("unknown %s %q (want auto or one of %s)", key, *v, rejectNames())
		}
	}
	return nil
}

// combineNames / rejectNames render the catalogue for an error message, so a typo tells the user
// exactly what they could have written.
func combineNames() string {
	var out []string
	for _, c := range stackalg.Combines() {
		out = append(out, string(c.ID))
	}
	return strings.Join(out, ", ")
}

func rejectNames() string {
	var out []string
	for _, r := range stackalg.Rejects() {
		out = append(out, string(r.ID))
	}
	return strings.Join(out, ", ")
}

// StackMenu is the algorithm catalogue the launch form's "Stacking & rejection" panel renders:
// which combination methods and rejection algorithms exist, which engine implements each, what
// their two parameters mean and default to, and which frame counts each is best at. Serving it from
// the engine (rather than duplicating it in TypeScript) is what keeps the dropdown from ever
// offering an algorithm the engine cannot run. nil for the modes that stack natively.
type StackMenu struct {
	Combines []stackalg.CombineInfo `json:"combines"`
	Rejects  []stackalg.RejectInfo  `json:"rejects"`
	Norms    []stackalg.Norm        `json:"norms"`
	Weights  []string               `json:"weights"`
	// MasterTypes are the calibration frame types that carry their own recipe, as wire prefixes.
	MasterTypes []string `json:"master_types"`
	// AutoBands explains the count-adaptive default in the same terms, so the panel can badge the
	// algorithm "auto" would pick for the capture in hand.
	AutoBands []StackAutoBand `json:"auto_bands"`
}

// StackAutoBand is one frame-count band of the automatic rejection rule.
type StackAutoBand struct {
	UpTo   int    `json:"up_to,omitempty"` // 0 = unbounded
	From   int    `json:"from,omitempty"`
	Reject string `json:"reject"`
}

// MasterFrameTypes are the calibration frame types the panel can configure, in the order it lists
// them. The keys are the wire prefix ("master_bias" → master_bias_reject …); "bias" and "offset"
// name the same frame, so the UI labels it once.
var MasterFrameTypes = []string{"master_bias", "master_dark", "master_flat", "master_dark_flat"}

// StackMenuFor returns the stacking catalogue for a mode, or nil when that mode stacks natively.
func StackMenuFor(m mode.Mode) *StackMenu {
	if !modeHasStackKnobs(m) {
		return nil
	}
	weights := make([]string, 0, len(stackalg.Weights()))
	for _, w := range stackalg.Weights() {
		weights = append(weights, displayWeight(w))
	}
	return &StackMenu{
		MasterTypes: append([]string(nil), MasterFrameTypes...),
		Combines:    stackalg.Combines(),
		Rejects:     stackalg.Rejects(),
		Norms:       stackalg.Norms(),
		Weights:     weights,
		AutoBands: []StackAutoBand{
			{UpTo: stackalg.AutoPercentileMax, Reject: string(stackalg.AutoReject(1))},
			{From: stackalg.AutoPercentileMax + 1, UpTo: stackalg.AutoGESDMin - 1, Reject: string(stackalg.AutoReject(stackalg.AutoPercentileMax + 1))},
			{From: stackalg.AutoGESDMin, Reject: string(stackalg.AutoReject(stackalg.AutoGESDMin))},
		},
	}
}
