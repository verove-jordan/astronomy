package scene3d

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeGalaxy reads the binary back the way the viewer does, so the test exercises the format rather
// than the sampler's internals.
type galaxyPoint struct {
	XPc, YPc, ZPc float64
	R, G, B, Lum  uint8
}

func decodeGalaxy(t *testing.T, buf []byte) []galaxyPoint {
	t.Helper()
	require.GreaterOrEqual(t, len(buf), galaxyHeaderSize)
	require.Equal(t, galaxyMagic, string(buf[:8]))
	require.EqualValues(t, GalaxyVersion, binary.LittleEndian.Uint16(buf[8:]))
	require.EqualValues(t, galaxyRecordSize, binary.LittleEndian.Uint16(buf[10:]))
	n := int(binary.LittleEndian.Uint32(buf[12:]))
	scale := float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[16:])))
	require.Equal(t, float64(galaxyPcPerUnit), scale)
	require.Len(t, buf, galaxyHeaderSize+n*galaxyRecordSize)

	out := make([]galaxyPoint, n)
	for i := range out {
		rec := buf[galaxyHeaderSize+i*galaxyRecordSize:]
		out[i] = galaxyPoint{
			XPc: float64(int16(binary.LittleEndian.Uint16(rec[0:]))) * scale,
			YPc: float64(int16(binary.LittleEndian.Uint16(rec[2:]))) * scale,
			ZPc: float64(int16(binary.LittleEndian.Uint16(rec[4:]))) * scale,
			R:   rec[6], G: rec[7], B: rec[8], Lum: rec[9],
		}
	}
	return out
}

func TestBuildGalaxyCloud_HasTheRequestedCount(t *testing.T) {
	pts := decodeGalaxy(t, buildGalaxyCloud(20000, galaxySeed))
	assert.Len(t, pts, 20000, "the record count must be exact however the population shares round")
}

// The cloud is served with an ETag. If the same seed produced different bytes, a client holding a
// cached copy would be told it was current when it was not.
func TestBuildGalaxyCloud_IsReproducible(t *testing.T) {
	a := buildGalaxyCloud(5000, galaxySeed)
	b := buildGalaxyCloud(5000, galaxySeed)
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, buildGalaxyCloud(5000, galaxySeed+1), "a different seed must give a different cloud")
}

// The structural checks: a sampled cloud has to reproduce the numbers it was built from. These are the
// tests that catch a sign error or a units slip, which no amount of looking at the picture will.
func TestBuildGalaxyCloud_ReproducesTheStructure(t *testing.T) {
	pts := decodeGalaxy(t, buildGalaxyCloud(60000, galaxySeed))

	var maxR, sumAbsZ float64
	inPlane, nearCentre, beyondSun := 0, 0, 0
	for _, p := range pts {
		r, _, zk := heliocentricToGalactocentric(p.XPc, p.YPc, p.ZPc)
		maxR = math.Max(maxR, r)
		sumAbsZ += math.Abs(zk)
		if math.Abs(zk) < thinScaleHeightKpc {
			inPlane++
		}
		if r < 2.5 {
			nearCentre++
		}
		if r > RSunKpc {
			beyondSun++
		}
	}

	assert.LessOrEqual(t, maxR, float64(haloMaxKpc)+0.5, "nothing may be sampled past the halo's edge")
	// The disc holds most of the mass and it is thin, so most points must be near the midplane.
	assert.Greater(t, float64(inPlane)/float64(len(pts)), 0.55,
		"a disc galaxy is mostly disc: %d of %d points within one thin scale height", inPlane, len(pts))
	// The bar and bulge are a fifth of the mass inside 2.5 kpc — a tiny volume — so the centre must be
	// the densest thing in the picture.
	assert.Greater(t, float64(nearCentre)/float64(len(pts)), 0.15,
		"the bulge and bar must dominate the inner few kiloparsec")
	// And it must not all be centre: the Sun is at 8.15 kpc with plenty of Galaxy outside it.
	assert.Greater(t, float64(beyondSun)/float64(len(pts)), 0.10,
		"there must be a substantial outer disc, not just a core")
}

// The carve-out is an honesty measure: no invented star may be dropped in among the run's own
// measured ones.
func TestBuildGalaxyCloud_LeavesTheSunsNeighbourhoodEmpty(t *testing.T) {
	for _, p := range decodeGalaxy(t, buildGalaxyCloud(60000, galaxySeed)) {
		d := math.Sqrt(p.XPc*p.XPc + p.YPc*p.YPc + p.ZPc*p.ZPc)
		require.GreaterOrEqual(t, d, float64(sunCarveOutPc)-galaxyPcPerUnit,
			"a model star landed %.0f pc from the Sun, among the field's measured stars", d)
	}
}

// The arm/interarm contrast is the number that decides whether this reads as a galaxy, and it is easy
// to get wrong in both directions. The first pass at the arms' share put it at twelve, which does not
// look like a spiral galaxy — it looks like bright wires laid on a smooth disc; drop it far enough and
// the arms vanish into the disc and there was no point using a real spiral model at all.
//
// Measured the way a photograph would: light per unit area, integrated through the disc, on the ridges
// against the gaps between them. Grand-design spirals sit at a few times in blue light, and these arms
// ARE the blue population.
func TestBuildGalaxyCloud_ShowsTheArmsAtALifelikeContrast(t *testing.T) {
	pts := decodeGalaxy(t, buildGalaxyCloud(GalaxyPoints, galaxySeed))

	var onRidge, between float64
	for _, p := range pts {
		r, _, zk := heliocentricToGalactocentric(p.XPc, p.YPc, p.ZPc)
		if r < 5 || r > DiscEdgeKpc-DiscFadeKpc || math.Abs(zk) > 1 {
			continue
		}
		_, off := nearestArmOffsetKpc(p.XPc, p.YPc, p.ZPc)
		lum := float64(p.Lum) / 255
		switch a := math.Abs(off); {
		case a < 0.35:
			onRidge += lum
		case a > 0.7 && a < 1.4:
			between += lum
		}
	}
	require.Greater(t, between, 100.0, "not enough inter-arm light to compare against")
	// The inter-arm band is twice as wide as the on-ridge one, so halve it to compare per unit area.
	contrast := onRidge / (between / 2)
	assert.Greater(t, contrast, 3.0, "the arms have washed into the disc (%.1fx)", contrast)
	assert.Less(t, contrast, 10.0, "the arms are bright wires, not arms (%.1fx)", contrast)
}

// A disc galaxy is centred on its centre. Not obvious to check by eye and easy to break: the arms are
// only FITTED over a third of the azimuth, so drawing them without continuing them round leaves the
// light a kiloparsec off-centre and the Galaxy visibly lopsided.
func TestBuildGalaxyCloud_IsCentredOnTheGalacticCentre(t *testing.T) {
	pts := decodeGalaxy(t, buildGalaxyCloud(GalaxyPoints, galaxySeed))

	var n, sx, sy float64
	for _, p := range pts {
		r, beta, _ := heliocentricToGalactocentric(p.XPc, p.YPc, p.ZPc)
		n++
		sx += r * math.Cos(beta*degToRad)
		sy += r * math.Sin(beta*degToRad)
	}
	assert.Less(t, math.Hypot(sx/n, sy/n), 0.5,
		"the light is centred %.2f kpc off the galactic centre", math.Hypot(sx/n, sy/n))
}

// Colour carries the population: the young arms are blue, the old bulge is warm. If those come out
// the same way round the picture is a grey disc.
func TestBuildGalaxyCloud_ColoursThePopulationsApart(t *testing.T) {
	pts := decodeGalaxy(t, buildGalaxyCloud(60000, galaxySeed))

	blueArm, warmCentre := 0, 0
	for _, p := range pts {
		r, _, zk := heliocentricToGalactocentric(p.XPc, p.YPc, p.ZPc)
		blue := int(p.B) > int(p.R)
		if r > 5 && r < DiscEdgeKpc-DiscFadeKpc && math.Abs(zk) < armScaleHeightKpc && blue {
			blueArm++
		}
		if r < 1.5 && !blue {
			warmCentre++
		}
	}
	assert.Greater(t, blueArm, 500, "the star-forming layer must hold plenty of blue stars")
	assert.Greater(t, warmCentre, 500, "the bulge must be warm-coloured")
}

func TestGalaxyCloud_IsMemoisedWithAStableETag(t *testing.T) {
	a, etagA := GalaxyCloud()
	b, etagB := GalaxyCloud()
	assert.Equal(t, etagA, etagB)
	assert.NotEmpty(t, etagA)
	require.Len(t, a, galaxyHeaderSize+GalaxyPoints*galaxyRecordSize)
	// Same backing array: the second call must not rebuild two megabytes.
	assert.Equal(t, &a[0], &b[0])
}

func TestHaloWeight_IsContinuousAcrossTheBreak(t *testing.T) {
	// The tolerance has to allow for the profile's own slope across the step, which is what makes this
	// a continuity check rather than a claim that the derivative matches too — it does not.
	const eps = 1e-6
	assert.InDelta(t, haloWeight(haloBreakKpc-eps), haloWeight(haloBreakKpc+eps), 1e-6)
	// And it must fall outward, or the halo would be an edge-brightened shell.
	assert.Greater(t, haloWeight(5), haloWeight(15))
	assert.Greater(t, haloWeight(15), haloWeight(30))
}

func TestGalaxyRNG_IsUniformAndNormal(t *testing.T) {
	g := newGalaxyRNG(1)
	var sum, sumSq float64
	const n = 20000
	for i := 0; i < n; i++ {
		u := g.float()
		require.GreaterOrEqual(t, u, 0.0)
		require.Less(t, u, 1.0)
		sum += u
	}
	assert.InDelta(t, 0.5, sum/n, 0.01)

	sum = 0
	for i := 0; i < n; i++ {
		v := g.normal()
		sum += v
		sumSq += v * v
	}
	assert.InDelta(t, 0, sum/n, 0.03)
	assert.InDelta(t, 1, sumSq/n, 0.05)
}
