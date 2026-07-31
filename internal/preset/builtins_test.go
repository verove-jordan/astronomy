package preset

import (
	"encoding/json"
	"testing"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
)

// validPalettes / validLooks / validBrightness mirror the launch-form option lists. They guard the
// top-level recipe fields the ApplyParamPatch check below does not see (those live in Params).
var (
	validPalettes   = set("natural", "hargb", "hoo", "sho", "hos", "foraxx", "mono")
	validLooks      = set("natural", "iphone", "deepsky")
	validBrightness = set("darker", "balanced", "brighter")
)

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// TestBuiltins_ApplyCleanly is the anti-drift guard: every built-in must parse its mode, carry only valid
// palette/look/brightness, and — crucially — its Params must apply through the SAME pipeline.ApplyParamPatch
// a real run uses with NO error and NO ignored keys. So a typo'd or renamed knob in the catalog fails here
// instead of silently doing nothing at runtime.
func TestBuiltins_ApplyCleanly(t *testing.T) {
	for _, it := range Builtins() {
		t.Run(it.Name, func(t *testing.T) {
			if !it.Builtin || it.ID != 0 {
				t.Fatalf("built-in %q must have Builtin=true and ID=0", it.Name)
			}
			var p Payload
			if err := json.Unmarshal(it.Payload, &p); err != nil {
				t.Fatalf("payload does not unmarshal: %v", err)
			}
			m, err := mode.ParseMode(p.Mode)
			if err != nil {
				t.Fatalf("mode %q does not parse: %v", p.Mode, err)
			}
			if p.Palette != "" && !validPalettes[p.Palette] {
				t.Errorf("unknown palette %q", p.Palette)
			}
			if p.Look != "" && !validLooks[p.Look] {
				t.Errorf("unknown look %q", p.Look)
			}
			if p.Brightness != "" && !validBrightness[p.Brightness] {
				t.Errorf("unknown brightness %q", p.Brightness)
			}
			if len(p.Params) == 0 {
				return
			}
			scratch := mode.For(m)
			res, err := pipeline.ApplyParamPatch(&scratch, p.Params)
			if err != nil {
				t.Fatalf("params do not apply for mode %s: %v", m, err)
			}
			if len(res.Ignored) > 0 {
				t.Errorf("params contain unknown knobs for mode %s: %v", m, res.Ignored)
			}
		})
	}
}

// TestBuiltins_SlugsUniqueAndComplete keeps the catalog the size the UI + i18n expect and guarantees the
// slugs (which key the i18n labels) are unique.
func TestBuiltins_SlugsUniqueAndComplete(t *testing.T) {
	items := Builtins()
	if len(items) != 21 {
		t.Fatalf("expected 21 built-in presets, got %d", len(items))
	}
	seen := map[string]bool{}
	for _, it := range items {
		if it.Name == "" {
			t.Error("built-in with empty slug")
		}
		if seen[it.Name] {
			t.Errorf("duplicate slug %q", it.Name)
		}
		seen[it.Name] = true
		if it.Category == "" {
			t.Errorf("built-in %q missing category", it.Name)
		}
	}
}
