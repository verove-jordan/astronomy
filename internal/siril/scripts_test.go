package siril

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// The default recipes every script builder was written against. Passing them must reproduce the
// exact command strings the engine emitted before stacking became configurable — that byte-identity
// is what the assertions in this file pin.
func lightOpts(w stackalg.Weight) stackalg.Options {
	o := stackalg.DefaultLights()
	o.Weight = w
	return o
}

func masterOpts() stackalg.Options { return stackalg.DefaultMasters().Dark }
func flatOpts() stackalg.Options   { return stackalg.DefaultMasters().Flat }

func TestRejection_AdaptsToFrameCount(t *testing.T) {
	// Verified against Siril 1.4.3 (see the live syntax test): percentile for tiny stacks,
	// winsorized in the mid range, GESD for large stacks. Unknown counts keep the proven default.
	tests := []struct {
		name string
		n    int
		want string
	}{
		{"unknown count keeps winsorized", 0, "rej winsorized 3 3"},
		{"tiny stack uses percentile", 7, "rej percentile 0.2 0.1"},
		{"mid range uses winsorized (low bound)", 8, "rej winsorized 3 3"},
		{"mid range uses winsorized (high bound)", 49, "rej winsorized 3 3"},
		{"large stack uses GESD", 50, "rej generalized 0.3 0.05"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Rejection(tt.n))
		})
	}
}

func TestScriptHeader_Pins32Bits(t *testing.T) {
	// Dark subtraction must keep negative pixels and the Go FITS readers require BITPIX -32, so the
	// bit depth must never depend on the host Siril's preferences.
	assert.Contains(t, StackMasterScript("cal", "/m/x", 10, masterOpts()), "set32bits\n")
}

func TestStackMasterScript(t *testing.T) {
	// `convert`, not `link`: a TIFF calibration set (SharpCap lunar darks) must stack like FITS.
	s := StackMasterScript("cal", "/m/master_bias", 30, masterOpts())
	assert.Contains(t, s, "convert cal -out=.")
	assert.Contains(t, s, "stack cal rej winsorized 3 3 -nonorm -out=/m/master_bias")
}

func TestStackMasterScript_LargePoolUsesGESD(t *testing.T) {
	s := StackMasterScript("cal", "/m/master_dark", 120, masterOpts())
	assert.Contains(t, s, "stack cal rej generalized 0.3 0.05 -nonorm -out=/m/master_dark")
}

func TestStackFlatScript_WithBias(t *testing.T) {
	s := StackFlatScript("cal", "/m/master_flat", "/m/master_bias.fits", 30, flatOpts())
	assert.Contains(t, s, "convert cal -out=.")
	assert.Contains(t, s, "calibrate cal -bias=/m/master_bias.fits -prefix=pp_")
	assert.Contains(t, s, "stack pp_cal rej winsorized 3 3 -norm=mul -out=/m/master_flat")
}

func TestStackFlatScript_NoBias(t *testing.T) {
	s := StackFlatScript("cal", "/m/master_flat", "", 30, flatOpts())
	assert.NotContains(t, s, "calibrate")
	assert.Contains(t, s, "stack cal rej winsorized 3 3 -norm=mul -out=/m/master_flat")
}

func TestStackFlatScript_FewFlatsUsePercentile(t *testing.T) {
	s := StackFlatScript("cal", "/m/master_flat", "", 5, flatOpts())
	assert.Contains(t, s, "stack cal rej percentile 0.2 0.1 -norm=mul -out=/m/master_flat")
}

func TestLightStackScript_FullCalibration(t *testing.T) {
	s := LightStackScript("light", CalibMasters{
		Dark: "/m/d.fits", Flat: "/m/f.fits", Bias: "/m/b.fits",
	}, "/out/master_L")
	// The bias is deliberately absent from the light calibration even though a master was supplied:
	// the dark already carries the pedestal and Siril would remove it twice. See calibrateArgs and
	// TestCalibrateArgs_BiasNeverDoubleSubtracted; this test's subject is the calibrate → register →
	// stack chain, which is unchanged.
	assert.Contains(t, s, "calibrate light -dark=/m/d.fits -flat=/m/f.fits -cc=dark -prefix=pp_")
	assert.Contains(t, s, "register pp_light")
	assert.Contains(t, s, "stack r_pp_light rej winsorized 3 3 -norm=addscale -output_norm -out=/out/master_L")
}

func TestLightStackScript_NoCalibration(t *testing.T) {
	s := LightStackScript("light", CalibMasters{}, "/out/master_L")
	assert.NotContains(t, s, "calibrate")
	assert.Contains(t, s, "register light")
	assert.Contains(t, s, "stack r_light")
	assert.True(t, strings.HasPrefix(s, "requires"))
}

func TestCalibrateSingleScript_UsesCalibrateSingleOnTheConvertedFrame(t *testing.T) {
	// A one-frame group gets no .seq from link (Siril has no one-image sequences), so the sequence
	// `calibrate` aborts — task #352. calibrate_single addresses the converted image directly and
	// must keep the pp_ output naming the callers derive with CalibratedSeq.
	s := CalibrateSingleScript("light", CalibMasters{
		Dark: "/m/d.fits", Flat: "/m/f.fits", Bias: "/m/b.fits",
	})
	assert.Contains(t, s, "link light -out=.")
	// No -bias here either: calibrate_single shares calibrateArgs, so the one-frame path gets the
	// same no-double-subtraction rule. The subject of this test is the calibrate_single naming.
	assert.Contains(t, s, "calibrate_single light_00001 -dark=/m/d.fits -flat=/m/f.fits -cc=dark -prefix=pp_")
	assert.NotContains(t, s, "calibrate light", "the sequence form would abort on a one-image conversion")
}

func TestCalibrateSingleScript_NoMastersLinksOnly(t *testing.T) {
	// With nothing to apply, the lone frame is only converted — the caller then uses light_00001
	// directly (CalibratedSeq returns the uncalibrated name).
	s := CalibrateSingleScript("light", CalibMasters{})
	assert.Contains(t, s, "link light -out=.")
	assert.NotContains(t, s, "calibrate")
}

func TestRegister2PassScript_ComputesWithoutApplying(t *testing.T) {
	// The cross-session merge, phase 1: metrics + homographies only — the Go review runs in between.
	s := Register2PassScript("light", "homography")
	assert.NotContains(t, s, "calibrate")
	assert.Contains(t, s, "register light -2pass -transf=homography")
	assert.NotContains(t, s, "seqapplyreg")
}

func TestApplyRegistrationScript_PinsAnchorAndCanvas(t *testing.T) {
	// Phase 2: the anchor-night reference frame pins the output canvas (framing=current).
	s := ApplyRegistrationScript("light", 42, "current")
	assert.Contains(t, s, "setref light 42")
	assert.Contains(t, s, "seqapplyreg light -framing=current")
	assert.NotContains(t, s, "register light")
}

func TestApplyRegistrationScript_ZeroRefKeepsSirilChoice(t *testing.T) {
	s := ApplyRegistrationScript("light", 0, "current")
	assert.NotContains(t, s, "setref")
	assert.Contains(t, s, "seqapplyreg light -framing=current")
}

func TestCalibrateStarAlignToRefScript_PinsMiddleFrame(t *testing.T) {
	s := CalibrateStarAlignToRefScript("light", CalibMasters{Dark: "/m/d.fits"}, 12)
	assert.Contains(t, s, "calibrate light -dark=/m/d.fits -cc=dark -prefix=pp_")
	assert.Contains(t, s, "setref pp_light 12")
	assert.Contains(t, s, "register pp_light -2pass")
	assert.Contains(t, s, "seqapplyreg pp_light")
}

func TestCalibrateStarAlignToRefScript_NoMastersClampsRef(t *testing.T) {
	s := CalibrateStarAlignToRefScript("light", CalibMasters{}, 0)
	assert.NotContains(t, s, "calibrate")
	assert.Contains(t, s, "setref light 1") // refIndex < 1 clamps to the first frame
	assert.Contains(t, s, "register light -2pass")
}

func TestPixelMathScript(t *testing.T) {
	sub := PixelMathScript("$stars$ - $starless$", "star_layer")
	assert.Contains(t, sub, `pm "$stars$ - $starless$"`)
	assert.Contains(t, sub, "save star_layer")

	screen := PixelMathScript("max($comet$, $stars$)", "final")
	assert.Contains(t, screen, `pm "max($comet$, $stars$)"`)
	assert.Contains(t, screen, "save final")
}

func TestCalibrateArgs_CFAOneShotColor(t *testing.T) {
	// One-shot-color with a full master set: CFA-aware cosmetics + flat equalization + debayer.
	//
	// This test used to assert that -bias was emitted here alongside -dark. That contract was the
	// bug: Siril subtracts both from the same frame, and a master dark already carries the bias
	// pedestal, so every light lost it twice (measured on Siril 1.4.4 — see calibrateArgs).
	s := CalibrateOnlyScript("osc", CalibMasters{Dark: "/m/d.fits", Flat: "/m/f.fits", Bias: "/m/b.fits", CFA: true})
	assert.Contains(t, s, "calibrate osc -dark=/m/d.fits -flat=/m/f.fits -cc=dark -cfa -equalize_cfa -debayer -prefix=pp_")
}

func TestCalibrateArgs_BiasNeverDoubleSubtracted(t *testing.T) {
	// Siril subtracts -bias AND -dark from the same frame. A master dark is an unnormalized exposure
	// that already contains the bias pedestal, so the two together remove it twice — and because the
	// constant is then divided by the flat, it returns as the flat's vignetting profile inverted, a
	// false gradient that was as large as the whole sky signal on the first ASI2600MC run.
	tests := []struct {
		name     string
		masters  CalibMasters
		wantBias bool
		why      string
	}{
		{
			name:     "a dark carries the pedestal, so the bias is not passed too",
			masters:  CalibMasters{Dark: "/m/d.fits", Flat: "/m/f.fits", Bias: "/m/b.fits"},
			wantBias: false,
			why:      "the dark already contains it",
		},
		{
			name:     "with no dark the bias is the only pedestal removal",
			masters:  CalibMasters{Flat: "/m/f.fits", Bias: "/m/b.fits"},
			wantBias: true,
			why:      "nothing else removes the pedestal",
		},
		{
			// -opt scales the dark's THERMAL part, which it can only isolate once the bias is gone.
			name:     "dark optimization needs both by design",
			masters:  CalibMasters{Dark: "/m/d300.fits", Bias: "/m/b.fits", DarkOptimize: true},
			wantBias: true,
			why:      "-opt cannot separate the thermal signal without it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := CalibrateOnlyScript("light", tt.masters)
			if tt.wantBias {
				assert.Contains(t, s, "-bias=/m/b.fits", tt.why)
			} else {
				assert.NotContains(t, s, "-bias=", tt.why)
			}
		})
	}
}

func TestCalibrateArgs_CFANoFlatOmitsEqualize(t *testing.T) {
	s := CalibrateOnlyScript("osc", CalibMasters{Dark: "/m/d.fits", CFA: true})
	assert.Contains(t, s, "calibrate osc -dark=/m/d.fits -cc=dark -cfa -debayer -prefix=pp_")
	assert.NotContains(t, s, "-equalize_cfa")
}

func TestCalibrateArgs_CFAWithoutMastersStillDebayers(t *testing.T) {
	// This test previously asserted that CFA with no masters emitted NOTHING. That was safe only
	// while no uncalibrated one-shot-color frames could reach this path; now that every mode stacks
	// colour, the common first-time case is a DSLR session with no darks or flats at all, and
	// skipping calibrate entirely left the mosaic undemosaiced — a green checkerboard all the way
	// through registration and stacking. The demosaic pass must run even with nothing to apply.
	s := CalibrateOnlyScript("osc", CalibMasters{CFA: true})
	assert.Contains(t, s, "calibrate osc -debayer -prefix=pp_")
	// -cfa/-equalize_cfa still make no sense with no master to make CFA-aware.
	assert.NotContains(t, s, "-cfa ")
	assert.NotContains(t, s, "-equalize_cfa")
}

func TestCalibrateArgs_MonoUnaffectedByCFAField(t *testing.T) {
	// A mono set (CFA false) must be byte-identical to before.
	s := CalibrateOnlyScript("light", CalibMasters{Dark: "/m/d.fits", Flat: "/m/f.fits"})
	assert.Contains(t, s, "calibrate light -dark=/m/d.fits -flat=/m/f.fits -cc=dark -prefix=pp_")
	assert.NotContains(t, s, "-cfa")
	assert.NotContains(t, s, "-debayer")
}

func TestRegisterOnlyScript(t *testing.T) {
	s := RegisterOnlyScript("pp_light")
	assert.True(t, strings.HasPrefix(s, "requires"))
	assert.Contains(t, s, "register pp_light\n")
	assert.NotContains(t, s, "link ")
	assert.NotContains(t, s, "calibrate")
}

func TestStackSelectedScript_Weighted(t *testing.T) {
	// The sequence is addressed exactly as Siril names it — frame prefix + trailing "_" — so the
	// script triggers no sequence-name-lookup recovery noise.
	s := StackSelectedScript("r_light", 10, []int{3}, "/out/master_L", lightOpts(stackalg.WeightWFWHM))
	assert.Contains(t, s, "select r_light_ 1 10")
	assert.Contains(t, s, "unselect r_light_ 3 3")
	assert.Contains(t, s, "stack r_light_ rej winsorized 3 3 -norm=addscale -output_norm -weight=wfwhm -filter-incl -out=/out/master_L")
}

func TestStackSelectedScript_UnweightedIsByteIdentical(t *testing.T) {
	// Empty weight must leave the single-session/OSC stack command exactly as it was.
	s := StackSelectedScript("r_light", 10, nil, "/out/master_L", lightOpts(stackalg.WeightNone))
	assert.Contains(t, s, "stack r_light_ rej winsorized 3 3 -norm=addscale -output_norm -filter-incl -out=/out/master_L")
	assert.NotContains(t, s, "-weight=")
}

func TestStackSelectedScript_RejectionSizedToSurvivors(t *testing.T) {
	// The rejection algorithm adapts to the frames actually stacked (registered minus graded-out),
	// not the registered count: 60 registered − 12 rejected = 48 survivors → winsorized, not GESD.
	s := StackSelectedScript("r_light", 60, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, "/out/m", lightOpts(stackalg.WeightWFWHM))
	assert.Contains(t, s, "rej winsorized 3 3")

	// All 60 kept → GESD kicks in for the large stack.
	s = StackSelectedScript("r_light", 60, nil, "/out/m", lightOpts(stackalg.WeightWFWHM))
	assert.Contains(t, s, "rej generalized 0.3 0.05")
}

func TestIntegrateChannelsScript_Weighted(t *testing.T) {
	// Links the staged, co-registered channel masters as sequence `synth` and stacks them. The stack
	// must address the linked sequence with its trailing separator ("synth_"), use addscale
	// normalization + sub-count weighting, and a gentle winsorized rejection (this Siril has no bare
	// "mean" — the average grammar needs a rejection method + two sigmas).
	s := IntegrateChannelsScript("synth", "/out/synthlum", stackalg.WeightNbStack)
	assert.True(t, strings.HasPrefix(s, "requires"))
	assert.Contains(t, s, "link synth -out=.\n")
	assert.Contains(t, s, "stack synth_ rej none -norm=addscale -output_norm -weight=nbstack -out=/out/synthlum\n")
	assert.NotContains(t, s, "register")
	assert.NotContains(t, s, "-norm=additive") // 'additive' is silently ignored by Siril; the token is 'add'/'addscale'
}

func TestIntegrateChannelsScript_Unweighted(t *testing.T) {
	// Empty weight → no -weight flag emitted (unweighted stack).
	s := IntegrateChannelsScript("synth", "/out/synthlum", stackalg.WeightNone)
	assert.Contains(t, s, "stack synth_ rej none -norm=addscale -output_norm -out=/out/synthlum\n")
	assert.NotContains(t, s, "-weight=")
}

func TestDenoiseScript(t *testing.T) {
	// -vst and -da3d are mutually exclusive in Siril; VST takes precedence.
	s := DenoiseScript("master_R.fits", "master_R", DenoiseOptions{Modulation: 0.8, VST: true, DA3D: true})
	assert.Contains(t, s, "load master_R.fits")
	assert.Contains(t, s, "denoise -vst -mod=0.80")
	assert.NotContains(t, s, "-da3d")
	assert.Contains(t, s, "save master_R")
}

func TestDenoiseScript_DA3DWhenNoVST(t *testing.T) {
	s := DenoiseScript("x.fits", "x", DenoiseOptions{Modulation: 0.7, DA3D: true})
	assert.Contains(t, s, "denoise -da3d -mod=0.70")
}

func TestDenoiseScript_NoFlagsWhenFullStrengthNoEngine(t *testing.T) {
	// modulation 1.0 (full) with no engine flags → bare `denoise`.
	s := DenoiseScript("x.fits", "x", DenoiseOptions{Modulation: 1})
	assert.Contains(t, s, "\ndenoise\n")
}

func TestPreviewScript(t *testing.T) {
	s := PreviewScript("master_L.fits", "L_preview", 0.5)
	assert.Contains(t, s, "load master_L.fits")
	assert.Contains(t, s, "resample 0.500")
	assert.Contains(t, s, "autostretch")
	assert.Contains(t, s, "savepng L_preview")
}

func TestColorCalibrateScript(t *testing.T) {
	s := ColorCalibrateScript("combined", "combined",
		SolveOptions{FocalMM: 740, PixelUm: 3.8, Catalog: "nomad"},
		SpccOptions{MonoSensor: "ZWO ASI1600MM Pro", RFilter: "Astronomik R", WhiteRef: "Average Spiral Galaxy"})
	assert.Contains(t, s, "load combined")
	assert.Contains(t, s, "platesolve -focal=740.0 -pixelsize=3.80 -catalog=nomad")
	// Siril 1.4.3's tokenizer needs the whole token quoted (see sirilKV), not just the value.
	assert.Contains(t, s, `spcc "-monosensor=ZWO ASI1600MM Pro" "-rfilter=Astronomik R" "-whiteref=Average Spiral Galaxy"`)
}

func TestColorCalibrateScript_HeaderCoordsOmitted(t *testing.T) {
	s := ColorCalibrateScript("c", "c", SolveOptions{FocalMM: 740, PixelUm: 3.8}, SpccOptions{})
	// no coords → platesolve relies on header WCS, so no leading coordinate token
	assert.Contains(t, s, "platesolve -focal=740.0")
	assert.NotContains(t, s, "platesolve  ")
}

func TestParityProbeScript(t *testing.T) {
	s := ParityProbeScript("pp_light_00001", "_parity_probe", SolveOptions{Coords: "210.8,54.3", FocalMM: 740, PixelUm: 3.8})
	assert.Contains(t, s, "load pp_light_00001")
	assert.Contains(t, s, "platesolve 210.8,54.3 -focal=740.0 -pixelsize=3.80 -noflip")
	assert.Contains(t, s, "save _parity_probe")
	assert.Equal(t, 1, strings.Count(s, "-noflip")) // -noflip so the probe pixels are not altered
}

func TestMirrorFramesScript(t *testing.T) {
	s := MirrorFramesScript([]string{"pp_light_00001", "pp_light_00002"})
	assert.Contains(t, s, "load pp_light_00001\nmirrorx\nsave pp_light_00001")
	assert.Contains(t, s, "load pp_light_00002\nmirrorx\nsave pp_light_00002")
	assert.Equal(t, 2, strings.Count(s, "mirrorx"))
}

func TestMirrorFramesScript_EmptyIsNoOp(t *testing.T) {
	s := MirrorFramesScript(nil)
	assert.NotContains(t, s, "mirrorx")
	assert.True(t, strings.HasPrefix(s, "requires"))
}

func TestNeutralizeScript(t *testing.T) {
	s := NeutralizeScript("combined", "combined", 1, 0)
	assert.Contains(t, s, "subsky 1")
	assert.Contains(t, s, "rmgreen 0")
	assert.Contains(t, s, "save combined")
}

func TestSubskyCmd_ClampsToSirilRange(t *testing.T) {
	tests := []struct {
		name   string
		degree int
		want   string
	}{
		{"zero clamps up to 1", 0, "subsky 1\n"},
		{"negative clamps up to 1", -2, "subsky 1\n"},
		{"in range passes through", 3, "subsky 3\n"},
		{"upper bound", 4, "subsky 4\n"},
		{"above range clamps to 4", 5, "subsky 4\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SubskyCmd(tt.degree))
		})
	}
}

func TestFinishScript_LinkedStretch(t *testing.T) {
	s := FinishScript("combined", "final", true, 0.15, []string{"png", "tif"})
	assert.Contains(t, s, "autostretch -linked")
	assert.Contains(t, s, "satu 0.15")
	assert.Contains(t, s, "savepng final")
	assert.Contains(t, s, "savetif final")
}

func TestColorCalibrateScript_LocalCatalogues(t *testing.T) {
	s := ColorCalibrateScript("c", "c",
		SolveOptions{FocalMM: 740, PixelUm: 3.8, AstroCat: "/lib/catalogues/siril_cat_healpix8_astro.dat", XpsampDir: "/lib/catalogues"},
		SpccOptions{MonoSensor: "ZWO ASI1600MM", Catalog: "localgaia"})
	// The set lines must precede the load so every solve/SPCC uses the offline data.
	assert.Contains(t, s, "set core.catalogue_gaia_astro=/lib/catalogues/siril_cat_healpix8_astro.dat\n")
	assert.Contains(t, s, "set core.catalogue_gaia_photo=/lib/catalogues\n")
	assert.Less(t, strings.Index(s, "set core.catalogue_gaia_astro"), strings.Index(s, "load c"))
	// An installed astro catalogue makes localgaia the default platesolve catalog.
	assert.Contains(t, s, "platesolve -focal=740.0 -pixelsize=3.80 -catalog=localgaia")
	assert.Contains(t, s, "-catalog=localgaia\nsave c")
}

func TestColorCalibrateScript_LocalCataloguePathWithSpaces(t *testing.T) {
	s := ColorCalibrateScript("c", "c",
		SolveOptions{AstroCat: "/My Library/cat.dat"}, SpccOptions{})
	// Whole-token quoting, same tokenizer rule as sirilKV.
	assert.Contains(t, s, "set \"core.catalogue_gaia_astro=/My Library/cat.dat\"\n")
}

func TestColorCalibrateScript_NoLocalCatalogues(t *testing.T) {
	s := ColorCalibrateScript("c", "c", SolveOptions{FocalMM: 740}, SpccOptions{})
	assert.NotContains(t, s, "set core.catalogue")
	assert.NotContains(t, s, "-catalog=")
}

func TestColorCalibrateScript_ExplicitCatalogWins(t *testing.T) {
	s := ColorCalibrateScript("c", "c",
		SolveOptions{Catalog: "nomad", AstroCat: "/lib/cat.dat"}, SpccOptions{})
	assert.Contains(t, s, "-catalog=nomad")
	assert.NotContains(t, s, "-catalog=localgaia")
}

func TestAlignPairScript_PinsReference(t *testing.T) {
	// The pair rescue must land the failed master on the ALREADY-ALIGNED reference's grid, so the
	// reference is pinned to image 1 — never left to Siril's quality-based choice — and star
	// detection is relaxed (the rescue targets weak masters whose dim stars the defaults miss).
	s := AlignPairScript("pair")
	assert.Contains(t, s, "link pair -out=.")
	assert.Contains(t, s, "setfindstar -sigma=0.5 -roundness=0.42 -relax=on")
	assert.Contains(t, s, "setref pair 1")
	assert.Contains(t, s, "register pair")
}

func TestCalibrateArgs_BadPixelMapReplacesDarkCosmetic(t *testing.T) {
	// A measured defect map repairs per frame via -cc=bpm (it also covers what -cc=dark would find);
	// the two cosmetic modes are exclusive.
	s := CalibrateOnlyScript("light", CalibMasters{Dark: "/m/d.fits", BadPixelMap: "/lib/master_DARK_defects.lst"})
	assert.Contains(t, s, "calibrate light -dark=/m/d.fits -cc=bpm /lib/master_DARK_defects.lst -prefix=pp_")
	assert.NotContains(t, s, "-cc=dark")
}

func TestCalibrateArgs_NoBPMFallsBackToDarkCosmetic(t *testing.T) {
	s := CalibrateOnlyScript("light", CalibMasters{Dark: "/m/d.fits"})
	assert.Contains(t, s, "-cc=dark")
	assert.NotContains(t, s, "-cc=bpm")
}

func TestCalibrateArgs_DarkOptimize(t *testing.T) {
	// A different-exposure dark is scaled with -opt — only when BOTH dark and bias are present.
	s := CalibrateOnlyScript("light", CalibMasters{Dark: "/m/d300.fits", Bias: "/m/b.fits", DarkOptimize: true})
	assert.Contains(t, s, "calibrate light -dark=/m/d300.fits -bias=/m/b.fits -cc=dark -opt -prefix=pp_")
}

func TestCalibrateArgs_DarkOptimizeNeedsBias(t *testing.T) {
	s := CalibrateOnlyScript("light", CalibMasters{Dark: "/m/d300.fits", DarkOptimize: true})
	assert.NotContains(t, s, "-opt")
}

func TestPlanetaryFinishScript_Headroom(t *testing.T) {
	// A headroom in (0,1) scales the linear image down (fmul) BEFORE the stretch so the bright disk keeps
	// structure instead of burning white; the fmul must precede the ght. 0 or 1 emit no fmul.
	fin := DefaultPlanetaryFinish()
	fin.Headroom = 0.85
	s := PlanetaryFinishScript("", "", "", "", "/m/master_mono", "/o/out", true, fin, []string{"png"})
	assert.Contains(t, s, "fmul 0.850")
	assert.Less(t, strings.Index(s, "fmul 0.850"), strings.Index(s, "ght "), "fmul precedes the stretch")

	for _, h := range []float64{0, 1} {
		fin.Headroom = h
		assert.NotContains(t, PlanetaryFinishScript("", "", "", "", "/m/master_mono", "/o/out", true, fin, []string{"png"}),
			"fmul", "headroom %v emits no fmul", h)
	}
}

func TestDefaultPlanetaryFinish_HasHeadroom(t *testing.T) {
	assert.InDelta(t, 0.85, DefaultPlanetaryFinish().Headroom, 1e-9)
}

func TestPlanetaryFinishScript_IgnoresEarthshineGain(t *testing.T) {
	// Pins the gain==0 byte-identity guarantee at its root: the finish script never varies with the
	// earthshine knob — the lift is a Go step between the finish and export scripts.
	fin := DefaultPlanetaryFinish()
	base := PlanetaryFinishScript("", "", "", "", "/m/master_mono", "/o/out", true, fin, []string{"png", "tif"})
	fin.EarthshineGain = 1.2
	assert.Equal(t, base, PlanetaryFinishScript("", "", "", "", "/m/master_mono", "/o/out", true, fin, []string{"png", "tif"}))
}

func TestPlanetaryFinishScript_ShadowLift(t *testing.T) {
	// shadow_lift slides the ght symmetry point SP = 0.18·(1−s) + 0.04·s into the shadows. s=0 must
	// emit the exact historical literal (byte-parity anchor); the descending SP values pin monotonicity.
	tests := []struct {
		name    string
		lift    float64
		wantGht string
	}{
		{"off is the historical literal", 0, "ght -D=0.600 -SP=0.18 -HP=0.850\n"},
		{"moderate lift", 0.35, "-SP=0.131"},
		{"half lift", 0.5, "-SP=0.110"},
		{"full lift", 1, "-SP=0.040"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fin := DefaultPlanetaryFinish()
			fin.ShadowLift = tt.lift
			s := PlanetaryFinishScript("", "", "", "", "/m/master_mono", "/o/out", true, fin, []string{"png"})
			assert.Contains(t, s, tt.wantGht)
		})
	}
}

func TestPlanetaryFinishScript_ShadowLiftChangesOnlyTheGhtLine(t *testing.T) {
	// A lift must move exactly one line — the ght stretch — and nothing else in the finish chain.
	fin := DefaultPlanetaryFinish()
	base := strings.Split(PlanetaryFinishScript("", "", "", "", "/m/mono", "/o/out", true, fin, []string{"png"}), "\n")
	fin.ShadowLift = 0.6
	lifted := strings.Split(PlanetaryFinishScript("", "", "", "", "/m/mono", "/o/out", true, fin, []string{"png"}), "\n")
	require.Equal(t, len(base), len(lifted), "line count is unchanged")
	diffs := 0
	for i := range base {
		if base[i] != lifted[i] {
			diffs++
			assert.True(t, strings.HasPrefix(lifted[i], "ght "), "the only changed line is the ght stretch, got %q", lifted[i])
		}
	}
	assert.Equal(t, 1, diffs, "exactly one line differs")
}

func TestPlanetarySplitFinish_MatchesSingleScript(t *testing.T) {
	// The split finish (compose + tone) is built from the same tone writer as the historical single
	// script, so the two can never drift. For mono the tone script IS the single script.
	fin := DefaultPlanetaryFinish()
	single := PlanetaryFinishScript("", "", "", "", "/m/mono", "/o/out", true, fin, []string{"png"})
	tone := PlanetaryToneScript("/m/mono", "/o/out", false, true, fin, []string{"png"})
	assert.Equal(t, single, tone)

	// TrueLum, like EarthshineGain, never changes the scripts themselves.
	off := fin
	off.TrueLum = false
	assert.Equal(t, single, PlanetaryFinishScript("", "", "", "", "/m/mono", "/o/out", true, off, []string{"png"}))

	c := PlanetaryComposeScript("/m/R", "/m/G", "/m/B", "/m/L", "/o/out")
	assert.Contains(t, c, "rgbcomp /m/R /m/G /m/B -lum=/m/L -out=/o/out\n")
	assert.NotContains(t, PlanetaryComposeScript("/m/R", "/m/G", "/m/B", "", "/o/out"), "-lum")
}

func TestExportScript(t *testing.T) {
	s := ExportScript("/o/out", []string{"png", "tif"})
	assert.Contains(t, s, "load /o/out\n")
	assert.Contains(t, s, "savepng /o/out\n")
	assert.Contains(t, s, "savetif /o/out\n")
	assert.Less(t, strings.Index(s, "load "), strings.Index(s, "savepng "), "load precedes the saves")
}

func TestAlignMastersScript_CommonFOV(t *testing.T) {
	// Channel masters register 2-pass and are applied with -framing=min: a channel with an offset
	// footprint (e.g. Ha) must not leave a zero-coverage strip that the layer stretch would turn into
	// a coloured band across the composite.
	s := AlignMastersScript("ch")
	assert.Contains(t, s, "link ch -out=.")
	assert.Contains(t, s, "register ch -2pass")
	assert.Contains(t, s, "seqapplyreg ch -framing=min")
}

func TestPhotometricCalibrateScript(t *testing.T) {
	s := PhotometricCalibrateScript("rgb_base", "rgb_base",
		SolveOptions{Coords: "170.06,12.99", FocalMM: 740, PixelUm: 3.8, AstroCat: "/lib/cat/astro.dat"})
	assert.Contains(t, s, "set core.catalogue_gaia_astro=/lib/cat/astro.dat")
	assert.Contains(t, s, "load rgb_base")
	assert.Contains(t, s, "platesolve 170.06,12.99 -focal=740.0 -pixelsize=3.80 -catalog=localgaia")
	// PCC is told the catalogue too. Bare `pcc` goes to the network ("Getting stars from online
	// catalogue NOMAD for PCC"), which makes the rung that survives SPCC's arm64 crash depend on the
	// internet at the moment a 40-minute run reaches its colour step; the local Gaia astrometry
	// catalogue already installed for the solve carries the photometry PCC needs.
	assert.Contains(t, s, "pcc -catalog=localgaia\n")
	assert.NotContains(t, s, "spcc", "the PCC rung must not invoke SPCC")

	// No local catalogue and no explicit choice: leave PCC to pick for itself (online NOMAD).
	bare := PhotometricCalibrateScript("rgb_base", "rgb_base", SolveOptions{Coords: "170.06,12.99"})
	assert.Contains(t, bare, "pcc\n")
	assert.NotContains(t, bare, "-catalog=")

	// An explicitly chosen catalogue wins for both commands.
	chosen := PhotometricCalibrateScript("rgb_base", "rgb_base",
		SolveOptions{Coords: "170.06,12.99", Catalog: "nomad", AstroCat: "/lib/cat/astro.dat"})
	assert.Contains(t, chosen, "pcc -catalog=nomad\n")
}

func TestFlattenRegister2PassScript(t *testing.T) {
	// One atomic script: link → per-frame background flatten → 2-pass register over the FLATTENED
	// sequence (a seqsubsky failure fails the whole script; the caller retries unflattened).
	s := FlattenRegister2PassScript("light", "homography", 1)
	assert.Contains(t, s, "link light -out=.")
	assert.Contains(t, s, "seqsubsky light 1 -prefix=flat_")
	assert.Contains(t, s, "register flat_light -2pass -transf=homography")
	assert.Equal(t, "flat_light", FlattenedSeq("light"))
}

func TestFlattenRegister2PassScript_ClampsDegree(t *testing.T) {
	assert.Contains(t, FlattenRegister2PassScript("light", "", 0), "seqsubsky light 1 ")
	assert.Contains(t, FlattenRegister2PassScript("light", "", 9), "seqsubsky light 4 ")
}

// The two-pass form exists because Siril's one-pass registration leaves the reference at frame 1 —
// wrong for any session that crosses the meridian. It must change the register line and NOTHING
// else: the ingest, the calibrate line and the resulting sequence names are what every downstream
// stage addresses, and finishStackedChannel reads the same <seq>_.seq either way.
func TestCalibrateRegister2PassScriptWith_ChangesOnlyTheRegisterLine(t *testing.T) {
	tests := []struct {
		name    string
		masters CalibMasters
		onePass string
		twoPass string
	}{
		{"calibrated sequence", CalibMasters{Dark: "master_dark.fits"}, "register pp_light\n", "register pp_light -2pass\n"},
		{"uncalibrated sequence", CalibMasters{}, "register light\n", "register light -2pass\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			one := CalibrateRegisterScriptWith("light", tt.masters, IngestLink)
			two := CalibrateRegister2PassScriptWith("light", tt.masters, IngestLink)

			require.True(t, strings.HasSuffix(one, tt.onePass), "one-pass script:\n%s", one)
			require.True(t, strings.HasSuffix(two, tt.twoPass), "two-pass script:\n%s", two)
			assert.Equal(t, strings.TrimSuffix(one, tt.onePass), strings.TrimSuffix(two, tt.twoPass),
				"only the register line may differ between the one- and two-pass forms")
		})
	}
}

// The two-pass path applies the registration with refIndex 0, which must leave the reference Siril
// elected in the metric pass alone — a setref here would put frame 1 back and undo the whole point.
func TestApplyRegistrationScript_ZeroRefIndexEmitsNoSetref(t *testing.T) {
	s := ApplyRegistrationScript("pp_light", 0, "current")

	assert.NotContains(t, s, "setref")
	assert.True(t, strings.HasSuffix(s, "seqapplyreg pp_light -framing=current\n"), "script:\n%s", s)
}
