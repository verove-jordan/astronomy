package pipeline

import (
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/siril"
)

func TestSmartObject(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"milkyway nested target/date/bucket", "input/MilkyWay/13_05_2026/Sorted_DNG", "MilkyWay"},
		{"date with dashes", "input/Andromeda/2026-05-13/lights", "Andromeda"},
		{"yyyymmdd date folder", "/data/M31/20260513", "M31"},
		{"plain target", "input/M101", "M101"},
		{"trailing slash", "input/MilkyWay/13_05_2026/Sorted_DNG/", "MilkyWay"},
		{"target with spaces", "input/Orion Nebula/Sorted_DNG", "Orion_Nebula"},
		{"all-generic degrades to leaf", "input/Sorted_DNG", "Sorted_DNG"},
		{"target is a year, not skipped", "input/2026/Sorted_DNG", "2026"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := smartObject(tt.in); got != tt.want {
				t.Fatalf("smartObject(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestResolveSolveCoords pins the plate-solve position-seed ladder (task #316: lights under
// .../triplet_m66/CapObj resolved to nothing — the leaf "CapObj" is a software placeholder and only
// the PARENT folder tokenizes to a catalogued name — so SPCC never ran and the stars came out green).
// CatalogDir is left "" so skycat falls back to its embedded snapshot: offline, deterministic.
func TestResolveSolveCoords(t *testing.T) {
	tests := []struct {
		name       string
		opts       Options
		object     string
		wantCoords bool
		wantSource string // "" = don't check; otherwise the source must contain it
	}{
		{
			name:       "configured coords win untouched",
			opts:       Options{Solve: siril.SolveOptions{Coords: "123.40000,45.60000"}},
			object:     "M101",
			wantCoords: true,
			wantSource: "configured",
		},
		{
			name:       "explicit target name",
			opts:       Options{TargetHint: "M66"},
			object:     "CapObj",
			wantCoords: true,
			wantSource: `target "M66"`,
		},
		{
			name:       "explicit target RA,Dec pair",
			opts:       Options{TargetHint: "170.06, +12.99"},
			object:     "CapObj",
			wantCoords: true,
			wantSource: "target",
		},
		{
			name:       "object name token (compound)",
			opts:       Options{},
			object:     "M81_M82_2020",
			wantCoords: true,
			wantSource: `object name "M81"`,
		},
		{
			name:       "the task #316 shape: placeholder leaf, catalogued parent",
			opts:       Options{InputDir: "input/2023_02_27/triplet_m66/CapObj"},
			object:     "CapObj",
			wantCoords: true,
			wantSource: `folder "triplet_m66"`,
		},
		{
			name: "multi-folder run: any input dir may carry the name",
			opts: Options{
				InputDir:  "input/2023_02_27/darks",
				InputDirs: []string{"input/2023_02_27/darks", "input/2020_04_26/M65_M66_NGC3628"},
			},
			object:     "CapObj",
			wantCoords: true,
			wantSource: `folder "M65_M66_NGC3628"`,
		},
		{
			name:       "date and generic segments are skipped, not resolved",
			opts:       Options{InputDir: "input/2023_02_27/lights"},
			object:     "CapObj",
			wantCoords: false,
		},
		{
			name:       "nothing resolvable stays empty",
			opts:       Options{InputDir: "input/backyard/CapObj", TargetHint: "NotACatalogueName"},
			object:     "CapObj",
			wantCoords: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coords, source := resolveSolveCoords(&tt.opts, tt.object)
			if (coords != "") != tt.wantCoords {
				t.Fatalf("resolveSolveCoords() coords = %q, want resolved=%v (source %q)", coords, tt.wantCoords, source)
			}
			if tt.wantSource != "" && !strings.Contains(source, tt.wantSource) {
				t.Fatalf("resolveSolveCoords() source = %q, want it to contain %q", source, tt.wantSource)
			}
		})
	}
}

// TestObjectCandidates_CompoundNames pins the embedded-designation extraction: separator-less
// compounds ("M81M82") must yield their catalogue tokens — the SPCC/annotation position-seed
// ladder failed on exactly this shape and the whole run lost photometric colour.
func TestObjectCandidates_CompoundNames(t *testing.T) {
	got := objectCandidates("M81M82")
	want := map[string]bool{"M81": false, "M82": false}
	for _, c := range got {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for tok, found := range want {
		if !found {
			t.Fatalf("candidates %v missing %s", got, tok)
		}
	}
	if cands := objectCandidates("NGC7023mosaic"); !contains(cands, "NGC7023") {
		t.Fatalf("NGC7023mosaic candidates %v missing NGC7023", cands)
	}
	if cands := objectCandidates("CapObj"); len(cands) != 1 {
		t.Fatalf("no false tokens expected on CapObj, got %v", cands)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
