package photom

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

// skyPlane builds a deterministic w×h SKY-DOMINATED plane: 85% sky pixels (pedestal + fixed-seed
// Gaussian noise) plus a 15% signal ramp tail, so its percentile span clears the flat-curve gate.
//
// CONTRACT-CHANGE JUSTIFICATION: replaces the old uniform-gradient fixture. A uniform distribution's
// MAD is ~25% of its range, so its P5..P97.5 span reads as "all noise" (below even pure Gaussian's
// 3.6σ) to the narrowband flat gate — the mixed-gain mis-measure fix. Real lights are sky-dominated
// with a signal tail, which is the premise the package itself is built on; the fixtures now model it.
func skyPlane(w, h int, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, w*h)
	for i := range out {
		if i%100 < 85 {
			out[i] = 0.2 + 0.01*float32(r.NormFloat64()) // sky: pedestal + noise
		} else {
			out[i] = 0.2 + 0.5*float32(i)/float32(len(out)) // signal tail: stars/nebulosity
		}
	}
	return out
}

// flatPlane builds a signal-free plane (pure sky pedestal + noise) — a narrowband fixture whose
// percentile curve is FLAT and must route through the metadata-seeded path, never a noise-width fit.
func flatPlane(w, h int, level float32, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, w*h)
	for i := range out {
		out[i] = level + level*0.02*float32(r.NormFloat64())
	}
	return out
}

// writePlane writes pix as a mono 32-bit-float FITS at path.
func writePlane(t *testing.T, path string, w, h int, pix []float32) {
	t.Helper()
	im := fits.NewImage(w, h, 1)
	copy(im.Pix[0], pix)
	require.NoError(t, im.WriteFITS(path))
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

func hasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestNormalizeGroups_ScalesGroupToReference(t *testing.T) {
	dir := t.TempDir()
	w, h := 64, 64
	apix := skyPlane(w, h, 12345)
	bpix := make([]float32, len(apix))
	for i, v := range apix {
		bpix[i] = 0.5*v + 0.01 // group B is a dim, offset copy of A
	}

	var aPaths, bPaths []string
	for i := 0; i < 3; i++ {
		ap := filepath.Join(dir, fmt.Sprintf("a%d.fits", i))
		bp := filepath.Join(dir, fmt.Sprintf("b%d.fits", i))
		writePlane(t, ap, w, h, apix)
		writePlane(t, bp, w, h, bpix)
		aPaths = append(aPaths, ap)
		bPaths = append(bPaths, bp)
	}

	groups := []Group{
		{Paths: aPaths, Label: "A", Ref: true},
		{Paths: bPaths, Label: "B"},
	}
	recs, notes := NormalizeGroups(context.Background(), groups)

	require.Len(t, recs, 2)
	assert.Equal(t, "A", recs[0].Label)
	assert.False(t, recs[0].Applied, "reference group is recorded but not rewritten")
	assert.Equal(t, "B", recs[1].Label)
	assert.True(t, recs[1].Applied, "group B is normalized onto the reference")
	assert.InDelta(t, 2.0, recs[1].Scale, 0.05, "recovers the inverse of B = 0.5*A + 0.01")
	assert.Empty(t, notes)

	aCurve, err := MeasureFile(aPaths[0])
	require.NoError(t, err)
	bCurve, err := MeasureFile(bPaths[0]) // re-read the rewritten B
	require.NoError(t, err)
	assert.InDelta(t, aCurve.Q[5], bCurve.Q[5], math.Abs(aCurve.Q[5])*0.01, "B's P50 lands within 1% of A's")
}

func TestNormalizeGroups_SkipsHomogeneous(t *testing.T) {
	dir := t.TempDir()
	w, h := 64, 64
	apix := skyPlane(w, h, 999)

	var aPaths, bPaths []string
	for i := 0; i < 2; i++ {
		ap := filepath.Join(dir, fmt.Sprintf("ha%d.fits", i))
		bp := filepath.Join(dir, fmt.Sprintf("hb%d.fits", i))
		writePlane(t, ap, w, h, apix)
		writePlane(t, bp, w, h, apix) // B == A ⇒ transform ~identity
		aPaths = append(aPaths, ap)
		bPaths = append(bPaths, bp)
	}
	before := readBytes(t, bPaths[0])

	groups := []Group{
		{Paths: aPaths, Label: "A", Ref: true},
		{Paths: bPaths, Label: "B"},
	}
	recs, _ := NormalizeGroups(context.Background(), groups)

	require.Len(t, recs, 2)
	assert.False(t, recs[1].Applied, "a homogeneous group is left untouched")
	assert.Equal(t, before, readBytes(t, bPaths[0]), "file bytes are unchanged")
}

func TestNormalizeGroups_Skips16BitGroup(t *testing.T) {
	dir := t.TempDir()
	w, h := 32, 32
	apix := skyPlane(w, h, 7)
	aPath := filepath.Join(dir, "ref.fits")
	writePlane(t, aPath, w, h, apix)

	// A 16-bit fixture with a clearly different distribution ⇒ a non-identity fit that reaches apply.
	pix16 := make([]uint16, w*h)
	for i := range pix16 {
		pix16[i] = uint16(1000 + i%5000)
	}
	cPath := fitstest.WritePixels(t, dir, "c16.fits", w, h, pix16, nil)
	before := readBytes(t, cPath)

	groups := []Group{
		{Paths: []string{aPath}, Label: "A", Ref: true},
		{Paths: []string{cPath}, Label: "C16"},
	}
	recs, notes := NormalizeGroups(context.Background(), groups)

	require.Len(t, recs, 2)
	assert.False(t, recs[1].Applied, "a 16-bit group cannot be rewritten in place")
	assert.Equal(t, before, readBytes(t, cPath), "16-bit file is left untouched")
	assert.True(t, hasNote(notes, "not 32-bit float"), "skip note is emitted; got %v", notes)
}

// TestNormalizeGroups_MetaSeededNarrowband is the end-to-end regression for the mixed-gain bug that
// had PhotomNorm disabled: two signal-free (flat-curve) narrowband groups at different ZWO gains.
// The old fit measured their NOISE-width ratio and clamped at 5×; now the scale is seeded from the
// header exposure/gain prediction (g250 → g400 ≈ 5.62×) and applied, with an explanatory note.
func TestNormalizeGroups_MetaSeededNarrowband(t *testing.T) {
	dir := t.TempDir()
	w, h := 64, 64
	const trueScale = 5.623 // 10^(150/200): ASI1600 gain 250 → 400
	apix := flatPlane(w, h, 0.2, 11)
	bpix := make([]float32, len(apix))
	for i, v := range apix {
		bpix[i] = v / trueScale // group B: the same sky shot at the lower gain
	}

	var aPaths, bPaths []string
	for i := 0; i < 2; i++ {
		ap := filepath.Join(dir, fmt.Sprintf("na%d.fits", i))
		bp := filepath.Join(dir, fmt.Sprintf("nb%d.fits", i))
		writePlane(t, ap, w, h, apix)
		writePlane(t, bp, w, h, bpix)
		aPaths = append(aPaths, ap)
		bPaths = append(bPaths, bp)
	}

	groups := []Group{
		{Paths: aPaths, Label: "Ha g400", Ref: true, Session: "2023-02-28",
			Meta: Meta{ExposureMs: 30000, Gain: 400, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}},
		{Paths: bPaths, Label: "Ha g250", SessionID: 42, Session: "2023-03-15",
			Meta: Meta{ExposureMs: 30000, Gain: 250, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}},
	}
	recs, notes := NormalizeGroups(context.Background(), groups)

	require.Len(t, recs, 2)
	assert.True(t, recs[0].Ref, "the reference group is marked for the UI")
	assert.Equal(t, "2023-02-28", recs[0].Session)
	assert.True(t, recs[1].MetaSeeded, "flat curves route through the metadata seed")
	assert.True(t, recs[1].Applied)
	assert.InDelta(t, trueScale, recs[1].Scale, trueScale*0.02, "scale IS the gain prediction, not a noise-width fit")
	assert.False(t, recs[1].Clamped, "the old 5.0 ceiling clamped exactly this case")
	assert.Equal(t, int64(42), recs[1].SessionID)
	assert.Equal(t, "2023-03-15", recs[1].Session)
	assert.True(t, hasNote(notes, "seeded from header exposure/gain"), "the seed is explained; got %v", notes)

	// The rewritten B group photometrically lands on A: its sky level matches within 2%.
	aCurve, err := MeasureFile(aPaths[0])
	require.NoError(t, err)
	bCurve, err := MeasureFile(bPaths[0])
	require.NoError(t, err)
	assert.InDelta(t, aCurve.Bg, bCurve.Bg, math.Abs(aCurve.Bg)*0.02, "B's sky pedestal lands on A's")
}

// TestDegradeClippingTransform pins the pre-apply no-clip gate (task #354: a ×8 mis-seed parked the
// 2019 sky at the reference level with 8× noise, flooring half its pixels to zero — black frames).
func TestDegradeClippingTransform(t *testing.T) {
	group := FrameCurve{Bg: 0.30, Noise: 0.004}
	ref := FrameCurve{Bg: 0.02, Noise: 0.002}

	tests := []struct {
		name         string
		in           Transform
		group, ref   FrameCurve
		wantMethod   string
		wantReverted bool
	}{
		{
			name: "healthy shrink passes untouched",
			// The corrected 2019 shape: ×0.05 lands the sky at ref level with SMALLER noise.
			in:    Transform{Scale: 0.05, Offset: 0.005, Method: MethodSeeded},
			group: group, ref: ref,
			wantMethod: MethodSeeded,
		},
		{
			name: "legit noise blow-up without clipping passes (no upper bound)",
			// A shallow night onto a deep reference: big scale is fine while the sky stays positive.
			in:    Transform{Scale: 6, Offset: 0.01, Method: MethodSeeded},
			group: FrameCurve{Bg: 0.05, Noise: 0.001}, ref: FrameCurve{Bg: 0.31, Noise: 0.02},
			wantMethod: MethodSeeded,
		},
		{
			name: "clipping transform degrades to the measured bg ratio",
			// ×8 parks the sky at +0.02 while 3·8·σ = 0.096 — floored. The bg ratio (0.0667) keeps
			// the sky at ref level with 3·0.0667·σ ≈ 0.0008 — safe.
			in:    Transform{Scale: 8, Offset: 0.02 - 8*0.30, Method: MethodSeeded},
			group: group, ref: ref,
			wantMethod: MethodBgMatched, wantReverted: true,
		},
		{
			name: "clipping transform with no usable backgrounds degrades to offset-only",
			// The reference sky sits below its own 2σ noise floor → bgScale refuses → offset-only.
			in:    Transform{Scale: 8, Offset: 0.001 - 8*0.30, Method: MethodSeeded},
			group: group, ref: FrameCurve{Bg: 0.001, Noise: 0.002},
			wantMethod: MethodOffsetOnly, wantReverted: true,
		},
		{
			name: "a sky that already straddles zero is not blamed on the transform",
			// Dark-subtracted near-zero backgrounds clip on their own (the two-night golden's
			// fixtures) — degrading a benign measured transform there would only lose information.
			in:    Transform{Scale: 1.02, Offset: -0.001, Method: MethodMeasured},
			group: FrameCurve{Bg: 0.001, Noise: 0.002}, ref: FrameCurve{Bg: 0.0011, Noise: 0.002},
			wantMethod: MethodMeasured,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, note := degradeClippingTransform("g", tt.in, tt.group, tt.ref)
			assert.Equal(t, tt.wantMethod, got.Method)
			if tt.wantReverted {
				require.NotEmpty(t, note, "a degraded transform must be explained")
				assert.Contains(t, note, "would clip its sky below zero")
				assert.False(t, clipsSky(got, tt.group) && got.Method == MethodBgMatched,
					"a bg-matched degrade must itself be clip-free")
			} else {
				assert.Empty(t, note)
				assert.Equal(t, tt.in, got, "a clean transform passes through unchanged")
			}
		})
	}
}

// zeroSkyPlane builds an over-subtracted reference sky: noise straddling ~zero, whose background
// sits BELOW its own 2σ floor — bgScale must refuse it, forcing the ladder past the bg rung.
func zeroSkyPlane(w, h int, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, w*h)
	for i := range out {
		out[i] = 0.0001 + 0.001*float32(r.NormFloat64())
	}
	return out
}

// TestNormalizeGroups_NoClipGateWiring proves the gate runs inside NormalizeGroups: a seeded ×8
// mis-scale onto a near-zero reference sky (no usable bg ratio) is degraded to offset-only — the
// group's pixels must never be amplified into the floor.
func TestNormalizeGroups_NoClipGateWiring(t *testing.T) {
	dir := t.TempDir()
	w, h := 64, 64
	refPix := zeroSkyPlane(w, h, 3)      // over-subtracted reference sky → bgScale refuses it
	brightPix := flatPlane(w, h, 0.5, 7) // the mis-seeded group: bright real sky

	var refPaths, gPaths []string
	for i := 0; i < 2; i++ {
		rp := filepath.Join(dir, fmt.Sprintf("ref%d.fits", i))
		gp := filepath.Join(dir, fmt.Sprintf("g%d.fits", i))
		writePlane(t, rp, w, h, refPix)
		writePlane(t, gp, w, h, brightPix)
		refPaths = append(refPaths, rp)
		gPaths = append(gPaths, gp)
	}
	groups := []Group{
		// Equal known gains ⇒ the prediction is the bare ×8 exposure ratio (the task #354 shape).
		{Paths: refPaths, Label: "deep", Ref: true,
			Meta: Meta{ExposureMs: 120000, Gain: 450, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}},
		{Paths: gPaths, Label: "shallow-bright",
			Meta: Meta{ExposureMs: 15000, Gain: 450, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}},
	}
	recs, notes := NormalizeGroups(context.Background(), groups)

	require.Len(t, recs, 2)
	assert.True(t, recs[1].Reverted, "the clipping ×8 seed must be degraded")
	assert.Equal(t, MethodOffsetOnly, recs[1].Method)
	assert.InDelta(t, 1.0, recs[1].Scale, 1e-9)
	assert.True(t, hasNote(notes, "would clip its sky below zero"), "the degrade is explained; got %v", notes)

	// Scale 1 was applied: the group's noise width is untouched (a surviving ×8 would read ~0.08).
	gc, err := MeasureFile(gPaths[0])
	require.NoError(t, err)
	assert.Less(t, gc.Noise, 0.02, "offset-only must not amplify the group's noise")
}
