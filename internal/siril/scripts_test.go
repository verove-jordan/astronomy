package siril

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
	assert.Contains(t, StackMasterScript("cal", "/m/x", 10), "set32bits\n")
}

func TestStackMasterScript(t *testing.T) {
	// `convert`, not `link`: a TIFF calibration set (SharpCap lunar darks) must stack like FITS.
	s := StackMasterScript("cal", "/m/master_bias", 30)
	assert.Contains(t, s, "convert cal -out=.")
	assert.Contains(t, s, "stack cal rej winsorized 3 3 -nonorm -out=/m/master_bias")
}

func TestStackMasterScript_LargePoolUsesGESD(t *testing.T) {
	s := StackMasterScript("cal", "/m/master_dark", 120)
	assert.Contains(t, s, "stack cal rej generalized 0.3 0.05 -nonorm -out=/m/master_dark")
}

func TestStackFlatScript_WithBias(t *testing.T) {
	s := StackFlatScript("cal", "/m/master_flat", "/m/master_bias.fits", 30)
	assert.Contains(t, s, "convert cal -out=.")
	assert.Contains(t, s, "calibrate cal -bias=/m/master_bias.fits -prefix=pp_")
	assert.Contains(t, s, "stack pp_cal rej winsorized 3 3 -norm=mul -out=/m/master_flat")
}

func TestStackFlatScript_NoBias(t *testing.T) {
	s := StackFlatScript("cal", "/m/master_flat", "", 30)
	assert.NotContains(t, s, "calibrate")
	assert.Contains(t, s, "stack cal rej winsorized 3 3 -norm=mul -out=/m/master_flat")
}

func TestStackFlatScript_FewFlatsUsePercentile(t *testing.T) {
	s := StackFlatScript("cal", "/m/master_flat", "", 5)
	assert.Contains(t, s, "stack cal rej percentile 0.2 0.1 -norm=mul -out=/m/master_flat")
}

func TestLightStackScript_FullCalibration(t *testing.T) {
	s := LightStackScript("light", CalibMasters{
		Dark: "/m/d.fits", Flat: "/m/f.fits", Bias: "/m/b.fits",
	}, "/out/master_L")
	assert.Contains(t, s, "calibrate light -dark=/m/d.fits -flat=/m/f.fits -bias=/m/b.fits -cc=dark -prefix=pp_")
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

func TestCalibrateRegisterFramedScript_CommonFOV(t *testing.T) {
	// The cross-session merge: no masters (already calibrated), rotation-aware homography, common-area crop.
	s := CalibrateRegisterFramedScript("light", CalibMasters{}, "homography", "min")
	assert.NotContains(t, s, "calibrate")
	assert.Contains(t, s, "register light -2pass -transf=homography")
	assert.Contains(t, s, "seqapplyreg light -framing=min")
}

func TestCalibrateRegisterFramedScript_WithMastersPrefixesTarget(t *testing.T) {
	s := CalibrateRegisterFramedScript("light", CalibMasters{Dark: "/m/d.fits"}, "affine", "cog")
	assert.Contains(t, s, "calibrate light -dark=/m/d.fits -cc=dark -prefix=pp_")
	assert.Contains(t, s, "register pp_light -2pass -transf=affine")
	assert.Contains(t, s, "seqapplyreg pp_light -framing=cog")
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
	s := CalibrateOnlyScript("osc", CalibMasters{Dark: "/m/d.fits", Flat: "/m/f.fits", Bias: "/m/b.fits", CFA: true})
	assert.Contains(t, s, "calibrate osc -dark=/m/d.fits -flat=/m/f.fits -bias=/m/b.fits -cc=dark -cfa -equalize_cfa -debayer -prefix=pp_")
}

func TestCalibrateArgs_CFANoFlatOmitsEqualize(t *testing.T) {
	s := CalibrateOnlyScript("osc", CalibMasters{Dark: "/m/d.fits", CFA: true})
	assert.Contains(t, s, "calibrate osc -dark=/m/d.fits -cc=dark -cfa -debayer -prefix=pp_")
	assert.NotContains(t, s, "-equalize_cfa")
}

func TestCalibrateArgs_CFAWithoutMastersEmitsNothing(t *testing.T) {
	// CFA only makes sense when there is a master to apply; with none, calibrate is skipped entirely.
	s := CalibrateOnlyScript("osc", CalibMasters{CFA: true})
	assert.NotContains(t, s, "calibrate")
	assert.NotContains(t, s, "-cfa")
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
	s := StackSelectedScript("r_light", 10, []int{3}, "/out/master_L", "wfwhm")
	assert.Contains(t, s, "select r_light 1 10")
	assert.Contains(t, s, "unselect r_light 3 3")
	assert.Contains(t, s, "stack r_light rej winsorized 3 3 -norm=addscale -output_norm -weight=wfwhm -filter-incl -out=/out/master_L")
}

func TestStackSelectedScript_UnweightedIsByteIdentical(t *testing.T) {
	// Empty weight must leave the single-session/OSC stack command exactly as it was.
	s := StackSelectedScript("r_light", 10, nil, "/out/master_L", "")
	assert.Contains(t, s, "stack r_light rej winsorized 3 3 -norm=addscale -output_norm -filter-incl -out=/out/master_L")
	assert.NotContains(t, s, "-weight=")
}

func TestStackSelectedScript_RejectionSizedToSurvivors(t *testing.T) {
	// The rejection algorithm adapts to the frames actually stacked (registered minus graded-out),
	// not the registered count: 60 registered − 12 rejected = 48 survivors → winsorized, not GESD.
	s := StackSelectedScript("r_light", 60, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, "/out/m", "wfwhm")
	assert.Contains(t, s, "rej winsorized 3 3")

	// All 60 kept → GESD kicks in for the large stack.
	s = StackSelectedScript("r_light", 60, nil, "/out/m", "wfwhm")
	assert.Contains(t, s, "rej generalized 0.3 0.05")
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

func TestAlignMastersScript_CommonFOV(t *testing.T) {
	// Channel masters register 2-pass and are applied with -framing=min: a channel with an offset
	// footprint (e.g. Ha) must not leave a zero-coverage strip that the layer stretch would turn into
	// a coloured band across the composite.
	s := AlignMastersScript("ch")
	assert.Contains(t, s, "link ch -out=.")
	assert.Contains(t, s, "register ch -2pass")
	assert.Contains(t, s, "seqapplyreg ch -framing=min")
}
