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

// gradientNoise builds a deterministic w×h plane: a horizontal gradient plus fixed-seed Gaussian noise,
// spanning roughly [0.15, 0.45] so its percentiles are well separated.
func gradientNoise(w, h int, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base := 0.2 + 0.2*float32(x)/float32(w)
			out[y*w+x] = base + 0.02*float32(r.NormFloat64())
		}
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
	apix := gradientNoise(w, h, 12345)
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
	apix := gradientNoise(w, h, 999)

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
	apix := gradientNoise(w, h, 7)
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
