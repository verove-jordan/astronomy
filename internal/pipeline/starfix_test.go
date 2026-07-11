package pipeline

import (
	"testing"

	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestProposeStarFix(t *testing.T) {
	base := mode.For(mode.Deepsky) // StretchHeadroom 0.90, HighlightCeil 0.92, Saturation 0.12
	good := starReport{Detected: 100, TrueSat: 0.3, TrueSpread: 0.1, FinalBurnt: 0.004, FinalWarm: 0.3, FinalSpread: 0.10}

	tests := []struct {
		name        string
		mod         func(*starReport)
		wantOK      bool
		wantTier    tier
		wantSetHR   bool // StretchHeadroom patched (the burnt path)
		wantSetSat  bool // Saturation patched (a colour path)
		wantExclude bool // HaExcludeStars patched (the warm path)
	}{
		{"clean → no proposal", func(r *starReport) {}, false, tierA, false, false, false},
		{"burnt → tier B headroom", func(r *starReport) { r.FinalBurnt = 0.05 }, true, tierB, true, false, false},
		{"warm → tier A colour", func(r *starReport) { r.FinalWarm = 0.8 }, true, tierA, false, true, true},
		{"flat → tier A saturation", func(r *starReport) { r.FinalSpread = 0.01 }, true, tierA, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := good
			tt.mod(&r)
			patch, ok := proposeStarFix(r, base)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			next := clampPreset(patch.apply(base))
			if got := tierOf(base, next); got != tt.wantTier {
				t.Errorf("tier = %v, want %v", got, tt.wantTier)
			}
			if (patch.StretchHeadroom != nil) != tt.wantSetHR {
				t.Errorf("StretchHeadroom set = %v, want %v", patch.StretchHeadroom != nil, tt.wantSetHR)
			}
			if (patch.Saturation != nil) != tt.wantSetSat {
				t.Errorf("Saturation set = %v, want %v", patch.Saturation != nil, tt.wantSetSat)
			}
			if (patch.HaExcludeStars != nil) != tt.wantExclude {
				t.Errorf("HaExcludeStars set = %v, want %v", patch.HaExcludeStars != nil, tt.wantExclude)
			}
		})
	}
}

func TestProposeStarFix_DiscsAndMottle(t *testing.T) {
	base := mode.For(mode.Deepsky) // StarDesat 0, ChromaBlur 0

	// Over-saturated colour discs (high final sat fraction, low TRUE star saturation) → a Tier-A
	// star_desat + saturation-trim pass.
	discs := starReport{Detected: 100, TrueSat: 0.05, FinalSatFrac: 0.6}
	patch, ok := proposeStarFix(discs, base)
	if !ok || patch.StarDesat == nil {
		t.Fatal("expected a star_desat proposal for colour discs")
	}
	if *patch.StarDesat <= base.StarDesat {
		t.Errorf("star_desat %.2f should increase from %.2f", *patch.StarDesat, base.StarDesat)
	}
	if got := tierOf(base, clampPreset(patch.apply(base))); got != tierA {
		t.Errorf("star_desat should be a Tier-A change, got %v", got)
	}
	if !discs.needsFix() {
		t.Error("over-saturated discs should trip needsFix")
	}

	// Genuine star colour (high TRUE saturation) is NOT treated as discs even at a high final fraction.
	genuine := starReport{Detected: 100, TrueSat: 0.5, FinalSatFrac: 0.6}
	if _, ok := proposeStarFix(genuine, base); ok {
		t.Error("a genuinely colourful field must not be desaturated as discs")
	}

	// Background chroma mottle → a Tier-A chroma_blur pass.
	mottle := starReport{Detected: 100, FinalBgChroma: 0.06}
	patch2, ok2 := proposeStarFix(mottle, base)
	if !ok2 || patch2.ChromaBlur == nil {
		t.Fatal("expected a chroma_blur proposal for background mottle")
	}
	if *patch2.ChromaBlur <= base.ChromaBlur {
		t.Errorf("chroma_blur %.2f should increase from %.2f", *patch2.ChromaBlur, base.ChromaBlur)
	}
	if !mottle.needsFix() {
		t.Error("background mottle should trip needsFix")
	}
}

func TestProposeStarFix_BurntAddsHeadroom(t *testing.T) {
	base := mode.For(mode.Deepsky)
	patch, ok := proposeStarFix(starReport{Detected: 100, FinalBurnt: 0.05, TrueSat: 0.3}, base)
	if !ok || patch.StretchHeadroom == nil {
		t.Fatal("expected a headroom proposal for burnt cores")
	}
	if *patch.StretchHeadroom >= base.StretchHeadroom {
		t.Errorf("headroom %.3f should be lower than the current %.3f (more protection)", *patch.StretchHeadroom, base.StretchHeadroom)
	}
	if *patch.StretchHeadroom < starFixHeadroomFloor {
		t.Errorf("headroom %.3f below the floor %.3f", *patch.StretchHeadroom, starFixHeadroomFloor)
	}
}

func TestCurrentHeadroom(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"off zero", 0, 1.0},
		{"off above one", 1.5, 1.0},
		{"active", 0.9, 0.9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mode.Preset{StretchHeadroom: tt.in}
			if got := currentHeadroom(p); got != tt.want {
				t.Errorf("currentHeadroom(%.2f) = %.2f, want %.2f", tt.in, got, tt.want)
			}
		})
	}
}

func TestComposeCeil(t *testing.T) {
	if got := composeCeil(mode.Preset{HighlightCeil: 0}); got != starFixDefaultCeil {
		t.Errorf("unset ceil = %.3f, want default %.3f", got, starFixDefaultCeil)
	}
	if got := composeCeil(mode.Preset{HighlightCeil: 0.92}); got != 0.92 {
		t.Errorf("ceil = %.3f, want 0.92", got)
	}
}

func TestStarFixIters(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"default", 0, starFixDefaultIters},
		{"capped", 10, starFixMaxIters},
		{"explicit", 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mode.Preset{StarFixMaxIters: tt.in}
			if got := starFixIters(&p); got != tt.want {
				t.Errorf("starFixIters(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
