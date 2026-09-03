package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// eclipse_test.go pins the eclipse preset against the solar one it is derived from.
//
// The mode exists to change the GEOMETRY, not the recipe, so every difference between the two presets
// has to be a deliberate one with a measured reason behind it. Pinning the derivation is what stops
// the two drifting apart silently — a solar tuning change that should reach eclipse but does not, or
// an eclipse override that quietly becomes the solar default.

func TestEclipsePreset_IsTheSolarPresetWhereTheGeometryIsTheSame(t *testing.T) {
	sun := mode.For(mode.Sun).Sun
	ecl := mode.For(mode.Eclipse).Sun

	if mode.For(mode.Eclipse).Mode != mode.Eclipse {
		t.Fatalf("eclipse preset is tagged %q", mode.For(mode.Eclipse).Mode)
	}

	// The finish is the solar finish in every respect but ONE — the palette. An eclipse run
	// re-renders through the same Refine panel and the same knob menu, so any other divergence is a
	// bug rather than a tuning choice, and comparing with the palette normalised away is what keeps
	// that true as either preset is tuned later.
	if ecl.Finish.Palette != solar.PaletteNative {
		t.Errorf("the eclipse palette is %q, want %q: the crescent has a colour the phone recorded, "+
			"and inventing a different one throws it away", ecl.Finish.Palette, solar.PaletteNative)
	}
	normalised := ecl.Finish
	normalised.Palette = sun.Finish.Palette
	if !reflect.DeepEqual(normalised, sun.Finish) {
		t.Errorf("the eclipse finish diverges from the solar finish somewhere other than the palette")
	}

	// Everything the eclipse geometry demands, and nothing else.
	for _, c := range []struct {
		name     string
		sun, ecl any
		why      string
	}{
		{"two_body", sun.TwoBody, ecl.TwoBody,
			"one circle handed a crescent's boundary points converges on a blend of the two bodies"},
		{"window_seconds", sun.WindowSeconds, ecl.WindowSeconds,
			"the Moon moves 9.8 px/min at 3.1\"/px, so 60 s smears its edge by 10 px against a ~2 px PSF"},
		{"window_frames", sun.WindowFrames, ecl.WindowFrames,
			"150 frames is five seconds at 30 fps and would split every window into six"},
		{"max_frames", sun.MaxFrames, ecl.MaxFrames, "same"},
		{"keep_percent", sun.KeepPercent, ecl.KeepPercent,
			"diffraction-limited, not seeing-limited: the stack is SNR-limited so depth beats selection"},
		{"drizzle", sun.Drizzle, ecl.Drizzle,
			"~2.5x undersampled with ample sub-pixel dither"},
	} {
		if c.sun == c.ecl {
			t.Errorf("%s is unchanged from the solar preset (%v), but the eclipse geometry needs it: %s",
				c.name, c.sun, c.why)
		}
	}

	if !ecl.TwoBody {
		t.Error("two_body is off in the eclipse preset, which is the one thing it exists to turn on")
	}
	if ecl.WindowSeconds > 40 {
		t.Errorf("window_seconds %.0f: past about 40 s the occulter's sweep exceeds the crescent's own detail scale",
			ecl.WindowSeconds)
	}
	// A window has to be allowed to hold the frames its duration implies, or the split is decided by
	// the frame cap and the seconds knob becomes decorative.
	if want := int(ecl.WindowSeconds * 30); ecl.WindowFrames < want {
		t.Errorf("window_frames %d cannot hold %.0f s at 30 fps (%d frames)",
			ecl.WindowFrames, ecl.WindowSeconds, want)
	}
	if ecl.MaxFrames < ecl.WindowFrames {
		t.Errorf("max_frames %d is below window_frames %d, so a source can never fill one window",
			ecl.MaxFrames, ecl.WindowFrames)
	}
}

// TestEclipse_ParsesAndCarriesTheSolarKnobSurface checks the wire-level plumbing: the mode is
// accepted, and it advertises the same knobs the solar mode does, since it shares the preset.
func TestEclipse_ParsesAndCarriesTheSolarKnobSurface(t *testing.T) {
	m, err := mode.ParseMode("eclipse")
	if err != nil {
		t.Fatalf("ParseMode(%q): %v", "eclipse", err)
	}
	if m != mode.Eclipse {
		t.Fatalf("ParseMode returned %q", m)
	}
	sunKeys := KnobRangesFor(mode.Sun)
	eclKeys := KnobRangesFor(mode.Eclipse)
	if len(eclKeys) != len(sunKeys) {
		t.Errorf("eclipse advertises %d knobs against the solar mode's %d; they share a preset and "+
			"must share a surface", len(eclKeys), len(sunKeys))
	}
	for k := range sunKeys {
		if _, ok := eclKeys[k]; !ok {
			t.Errorf("knob %q is offered for sun but not for eclipse", k)
		}
	}
	if KnobMenuFor(mode.Eclipse) != KnobMenuFor(mode.Sun) {
		t.Error("the eclipse knob menu diverges from the solar one")
	}
}

// TestEclipse_ReachesEveryModeSwitchSunDoes is the guard for the dispatch site that was missed.
//
// A new mode has to be added in several places, and the failure mode of missing one is not a
// compile error — it is falling through to the default branch, which is deep-sky. That is how a
// refine on an eclipse run went looking for `aligned_*` channel masters a solar run has never
// written, and failed with a message naming the one thing that had nothing to do with it.
//
// Rather than list the sites, this asserts the property they all share: wherever Sun is handled,
// Eclipse must be handled the same way, because it runs the same pipeline over the same artefacts.
func TestEclipse_ReachesEveryModeSwitchSunDoes(t *testing.T) {
	for _, c := range []struct {
		name       string
		sun, eclip any
	}{
		{"knob menu", KnobMenuFor(mode.Sun), KnobMenuFor(mode.Eclipse)},
		{"stack knobs", modeHasStackKnobs(mode.Sun), modeHasStackKnobs(mode.Eclipse)},
	} {
		if !reflect.DeepEqual(c.sun, c.eclip) {
			t.Errorf("%s: eclipse is handled differently from sun", c.name)
		}
	}
	// The knob surfaces have to agree key for key, since they share a preset.
	sun, eclip := ParamsFor(mode.For(mode.Sun)), ParamsFor(mode.For(mode.Eclipse))
	for k := range sun {
		if _, ok := eclip[k]; !ok {
			t.Errorf("params key %q is offered for sun but not for eclipse", k)
		}
	}
	// And refine must not fall through to the deep-sky reconstruction. A run directory with none of
	// the artefacts either path wants makes both fail — but with different messages, and only the
	// solar one is the right failure.
	opts := Options{Preset: func() *mode.Preset { p := mode.For(mode.Eclipse); return &p }()}
	_, err := RefineExistingRun(context.Background(), opts, t.TempDir())
	if err == nil {
		t.Fatal("refine succeeded on an empty directory")
	}
	if strings.Contains(err.Error(), "aligned_") {
		t.Errorf("refine on an eclipse run fell through to the deep-sky path: %v", err)
	}
}

// TestEclipse_SequenceIsOffUntilAskedFor pins the sheet's two safety properties: an existing eclipse
// run renders exactly what it always did, and a solar run cannot accidentally ask for a sequence of
// an eclipse it does not contain.
//
// The layout knobs live on the SHARED preset rather than on the eclipse one so the two modes keep
// the identical knob surface TestEclipse_ParsesAndCarriesTheSolarKnobSurface requires. They are
// inert on both until sequence_panels is set, and inert on sun whatever it is set to.
func TestEclipse_SequenceIsOffUntilAskedFor(t *testing.T) {
	for _, m := range []mode.Mode{mode.Sun, mode.Eclipse} {
		p := mode.For(m)
		if p.Sun.SequencePanels != 0 {
			t.Errorf("%s asks for %d sequence panels by default", m, p.Sun.SequencePanels)
		}
		if p.Sun.WantsSequence() {
			t.Errorf("%s wants a sequence by default", m)
		}
	}

	tests := []struct {
		name    string
		mode    mode.Mode
		params  string
		want    bool
		wantPan int
	}{
		{"eclipse asked for a sheet", mode.Eclipse, `{"sequence_panels":11}`, true, 11},
		{"sun asked for a sheet has no occulter to sequence", mode.Sun, `{"sequence_panels":11}`, false, 11},
		{"sun told to look for an occulter can sequence one", mode.Sun, `{"sequence_panels":9,"two_body":true}`, true, 9},
		{"a request below three panels is not a sequence", mode.Eclipse, `{"sequence_panels":1}`, true, 3},
		{"zero stays reachable so the sheet can be turned off", mode.Eclipse, `{"sequence_panels":0}`, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mode.For(tt.mode)
			if _, err := ApplyParamPatch(&p, json.RawMessage(tt.params)); err != nil {
				t.Fatalf("ApplyParamPatch: %v", err)
			}
			if got := p.Sun.WantsSequence(); got != tt.want {
				t.Errorf("WantsSequence() = %v, want %v", got, tt.want)
			}
			if p.Sun.SequencePanels != tt.wantPan {
				t.Errorf("SequencePanels = %d, want %d", p.Sun.SequencePanels, tt.wantPan)
			}
		})
	}
}

// TestEclipse_AskingForASheetMergesTheScaleGroups is the trap this feature would otherwise walk into
// every time. The clips of an eclipse are shot over an hour or more at whatever magnification the
// phone happened to be at — the 12 Aug 2026 session triages into three groups at 824, 595 and
// 547 px — and left separate, only one of them would ever reach the sheet.
func TestEclipse_AskingForASheetMergesTheScaleGroups(t *testing.T) {
	p := mode.For(mode.Eclipse)
	if p.Sun.RescaleGroups {
		t.Fatal("the eclipse preset rescales groups by default; this test would prove nothing")
	}
	if _, err := ApplyParamPatch(&p, json.RawMessage(`{"sequence_panels":9}`)); err != nil {
		t.Fatalf("ApplyParamPatch: %v", err)
	}
	if !p.Sun.RescaleGroups {
		t.Error("asking for a sequence must bring every scale group onto one canonical disc")
	}
}
