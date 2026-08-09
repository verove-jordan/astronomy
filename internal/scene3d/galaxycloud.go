// The Galaxy as a point cloud: one binary the browser fetches once and hands straight to the GPU.
//
// A galaxy IS stars, and drawing it as stars is both prettier and more honest than drawing it as
// translucent sheets — the vertical structure (a razor-thin star-forming layer inside a thin disc
// inside a puffy thick disc inside a halo) becomes visible, and flying through it gives real
// parallax instead of a poster that slides.
//
// The cloud is run-INDEPENDENT: it is the Galaxy, not this photograph's Galaxy. Only the rotation
// into a run's own frame is per-run, and that is a 3×3 uniform. So this is generated once per
// process, served with a long cache lifetime, and uploaded to the GPU once per session however many
// runs the user opens.
//
// Sampling is by MASS: every point stands for the same amount of stellar mass, so the density
// contrast between the bulge, the arms and the inter-arm disc comes out of how many points land
// there rather than out of a brightness fudge. Each point then carries its population's
// surface-brightness weight, which is where the difference between old red bulge stars and young blue
// arm stars belongs.
//
// One thing this does NOT model is dust. Extinction depends on where the observer stands — the same
// cloud reddens what is behind it and leaves what is in front of it alone — so faking it with a
// darkening pass would dim the near side of the bulge exactly as readily as the far side. Doing it
// properly needs a 3D dust map and a per-frame integration along every line of sight. The dark lanes
// a real photograph shows are therefore absent; what is there instead is the true density contrast
// between the arms and the gaps, which is a fact rather than a guess.
package scene3d

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
)

// The binary the viewer decodes. Little-endian, fixed-width, and laid out so every attribute sits at
// an offset its own type can address — which is what gl.vertexAttribPointer requires.
//
//	header, 32 bytes:
//	 0  magic      "ASTROGXY"
//	 8  version    uint16
//	10  recordSize uint16
//	12  count      uint32
//	16  pcPerUnit  float32   what one position unit is worth, in parsec
//	20  seed       uint32    the sampler's seed, so a cloud can be reproduced exactly
//	24  reserved   8 bytes, zero
//
//	record, 10 bytes:
//	 0  x   int16   heliocentric galactic, toward the galactic centre
//	 2  y   int16   toward the direction of galactic rotation
//	 4  z   int16   toward the north galactic pole
//	 6  r   uint8   the population's blackbody hue
//	 7  g   uint8
//	 8  b   uint8
//	 9  lum uint8   the population's surface-brightness weight
//
// Versioned and self-describing: a reader meeting an unknown version or record size must refuse the
// file rather than misread it, exactly like scene3d.bin and internal/deepstars/format.go.
const (
	galaxyMagic      = "ASTROGXY"
	GalaxyVersion    = 1
	galaxyHeaderSize = 32
	galaxyRecordSize = 10
)

// galaxyPcPerUnit is the position quantum. 2 pc reaches ±65 kpc in an int16 — past the halo, with
// room to spare — at a resolution four hundred times finer than the gap between neighbouring points,
// so the quantisation can never be seen.
const galaxyPcPerUnit = 2

// GalaxyPoints is how many points the cloud holds. Enough that the disc reads as continuous from
// outside it at 1080p, small enough that the whole thing is under two megabytes and uploads in one
// call.
const GalaxyPoints = 180000

// galaxySeed fixes the sampler. The cloud must be byte-identical from run to run and from process to
// process: it is served with an ETag, and a client that already has it must not be handed a different
// Galaxy on a re-fetch.
const galaxySeed = 0x5eedca11

// --- populations ---------------------------------------------------------------------------------

// population is one structural component: what share of the points it takes, how bright a point of
// it is, what colour those points are, and how to draw a position from it.
//
// share is the component's share of the Galaxy's STELLAR MASS (Bland-Hawthorn & Gerhard 2016, Table
// 5-ish: the thin disc holds around two thirds, the bulge and bar a quarter between them, the thick
// disc a tenth and the halo well under a per cent). The thin disc's share is split between the smooth
// disc, the arms drawn inside it and the star-forming knots along them.
//
// lum is a relative surface-brightness weight, and it is the one number here that is tuned rather
// than cited. It stands in for the mass-to-light ratio, which really does differ several-fold between
// a young arm and an old bulge; the ordering is physical, the exact values are chosen so the picture
// reads the way a deep photograph of a spiral does.
type population struct {
	key   string
	share float64
	lum   float64
	// bv is the population's mean colour index and its spread. Zero sigma means every point takes the
	// mean. A nil colour means the population is not a blackbody at all.
	bvMean  float64
	bvSigma float64
	rgb     *[3]uint8
	// sample draws one position in galactocentric cylindrical coordinates. ok is false when the draw
	// fell outside the component and must be retried — the samplers use rejection freely, because it
	// is far easier to read than an inverted CDF and this runs once.
	sample func(g *galaxyRNG) (rKpc, betaDeg, zKpc float64, ok bool)
}

// hiiRGB is the colour of a giant star-forming region. Not a blackbody: it is line emission, Hα with
// enough Hβ and [O III] to read as pink rather than as the deep red of Hα alone.
var hiiRGB = [3]uint8{255, 118, 150}

func galaxyPopulations() []population {
	return []population{
		{
			key: "bulge", share: 0.200, lum: 0.55, bvMean: 0.92, bvSigma: 0.14,
			sample: func(g *galaxyRNG) (float64, float64, float64, bool) {
				return g.superEllipsoid(bulgeSemiKpc, bulgeBoxiness, 2.5)
			},
		},
		{
			key: "bar", share: 0.035, lum: 0.50, bvMean: 0.90, bvSigma: 0.14,
			sample: func(g *galaxyRNG) (float64, float64, float64, bool) {
				return g.superEllipsoid(barSemiKpc, barBoxiness, 1.5)
			},
		},
		{
			key: "thinDisc", share: 0.580, lum: 0.34, bvMean: 0.68, bvSigma: 0.28,
			sample: func(g *galaxyRNG) (float64, float64, float64, bool) {
				return g.disc(thinScaleLengthKpc, thinScaleHeightKpc)
			},
		},
		{
			// The arms' share is set by the arm/interarm CONTRAST it produces, not by guesswork: measured
			// across the disc this comes out near six times, which is where a grand-design spiral sits in
			// blue light — and these are the blue population. The first pass at it was twelve, and twelve
			// does not look like a galaxy; it looks like bright wires laid on a smooth disc.
			key: "arms", share: 0.065, lum: 1.00, bvMean: 0.05, bvSigma: 0.22,
			sample: func(g *galaxyRNG) (float64, float64, float64, bool) {
				return g.arm(0)
			},
		},
		{
			key: "hii", share: 0.012, lum: 1.00, rgb: &hiiRGB,
			sample: func(g *galaxyRNG) (float64, float64, float64, bool) {
				// A knot is a clump, not a point: the extra scatter is small compared with the arm's
				// own width, so the knots sit ON the ridge and read as beads along it.
				return g.arm(0.05)
			},
		},
		{
			key: "thickDisc", share: 0.100, lum: 0.20, bvMean: 0.85, bvSigma: 0.16,
			sample: func(g *galaxyRNG) (float64, float64, float64, bool) {
				return g.disc(thickScaleLengthKpc, thickScaleHeightKpc)
			},
		},
		{
			key: "halo", share: 0.008, lum: 0.14, bvMean: 0.70, bvSigma: 0.20,
			sample: func(g *galaxyRNG) (float64, float64, float64, bool) {
				return g.halo()
			},
		},
	}
}

// --- the samplers --------------------------------------------------------------------------------

// disc draws from an exponential surface density with an isothermal-sheet height: ρ ∝ exp(−R/hR)
// sech²(z/hZ), truncated at the disc edge with the cosine fade.
//
// The radius comes from a gamma(2) draw, which is exactly p(R) ∝ R·exp(−R/hR) — the surface density
// times the area of its annulus — and the height from the closed-form inverse of the sech² profile.
// Neither needs rejection; the edge fade does.
func (g *galaxyRNG) disc(hR, hZ float64) (float64, float64, float64, bool) {
	r := -hR * (math.Log(g.unit()) + math.Log(g.unit()))
	if r > DiscEdgeKpc {
		return 0, 0, 0, false
	}
	if g.float() > discEdgeFade(r) {
		return 0, 0, 0, false
	}
	return r, g.float() * 360, hZ * math.Atanh(2*g.unit()-1), true
}

// arm draws a point along one of the Reid et al. loci, scattered across the arm's own fitted width
// and confined to the thin star-forming layer. extraKpc adds an isotropic clump for the knots.
func (g *galaxyRNG) arm(extraKpc float64) (float64, float64, float64, bool) {
	a := g.pickArm()
	lo, hi := armSweep(a)
	beta := lo + g.float()*(hi-lo)
	// The weight thins the continued part of the arm instead of dimming it: fewer stars is what
	// "less certain that there are stars here" looks like.
	if g.float() > armWeight(a, beta) {
		return 0, 0, 0, false
	}
	r := armLocus(a, beta) + a.widthKpc*g.normal() + extraKpc*g.normal()
	if r < armInnerCutKpc || r > DiscEdgeKpc {
		return 0, 0, 0, false
	}
	if g.float() > discEdgeFade(r) {
		return 0, 0, 0, false
	}
	z := armScaleHeightKpc*math.Atanh(2*g.unit()-1) + extraKpc*g.normal()
	// The azimuthal clump has to be an ANGLE, and at 10 kpc a tenth of a kiloparsec is well under a
	// degree — so scattering β by a fixed number of degrees would make the knots grow with radius.
	if extraKpc > 0 && r > 0 {
		beta += (extraKpc * g.normal() / r) / degToRad
	}
	return r, beta, z, true
}

// pickArm chooses an arm in proportion to how much azimuth it is drawn over, so a long arm gets its
// share of the points rather than every arm getting the same number spread over a different length.
func (g *galaxyRNG) pickArm() arm {
	span := func(a arm) float64 {
		lo, hi := armSweep(a)
		return hi - lo
	}
	total := 0.0
	for _, a := range arms {
		total += span(a)
	}
	x := g.float() * total
	for _, a := range arms {
		x -= span(a)
		if x <= 0 {
			return a
		}
	}
	return arms[len(arms)-1]
}

// superEllipsoid draws from a boxy bar-like component: uniform inside the super-ellipsoid its
// semi-axes bound, thinned toward the outside by exp(−concentration·m), then rotated to the bar
// angle. The outline is the measured axis ratio; the fall-off inside it is the model.
func (g *galaxyRNG) superEllipsoid(semi [3]float64, boxiness, concentration float64) (float64, float64, float64, bool) {
	x := (2*g.float() - 1) * semi[0]
	y := (2*g.float() - 1) * semi[1]
	z := (2*g.float() - 1) * semi[2]
	m := math.Pow(
		math.Pow(math.Abs(x/semi[0]), boxiness)+
			math.Pow(math.Abs(y/semi[1]), boxiness)+
			math.Pow(math.Abs(z/semi[2]), boxiness),
		1/boxiness,
	)
	if m > 1 || g.float() > math.Exp(-concentration*m) {
		return 0, 0, 0, false
	}
	// The bar's near end is at positive galactic longitude, so its major axis lies at β = +27°.
	c, s := math.Cos(barAngleDeg*degToRad), math.Sin(barAngleDeg*degToRad)
	gx, gy := x*c-y*s, x*s+y*c
	return math.Hypot(gx, gy), math.Atan2(gy, gx) / degToRad, z, true
}

// halo draws from the broken power law, isotropically about the galactic centre. Rejection against
// the profile's peak, which is at the inner cut.
func (g *galaxyRNG) halo() (float64, float64, float64, bool) {
	r := haloMinKpc + g.float()*(haloMaxKpc-haloMinKpc)
	if g.float() > haloWeight(r)/haloWeight(haloMinKpc) {
		return 0, 0, 0, false
	}
	// Uniform on the sphere: the cosine of the polar angle is what must be uniform, not the angle.
	cosTheta := 2*g.float() - 1
	sinTheta := math.Sqrt(math.Max(0, 1-cosTheta*cosTheta))
	phi := g.float() * 2 * math.Pi
	return r * sinTheta, phi / degToRad, r * cosTheta, true
}

// haloWeight is r²ρ(r) — the mass per unit radius, which is what a radial draw has to follow.
func haloWeight(r float64) float64 {
	if r <= 0 {
		return 0
	}
	if r <= haloBreakKpc {
		return math.Pow(r, 2-haloInnerIndex)
	}
	// Continuous across the break: the outer branch starts where the inner one ended.
	inner := math.Pow(haloBreakKpc, 2-haloInnerIndex)
	return inner * math.Pow(r/haloBreakKpc, 2-haloOuterIndex)
}

// --- generation ----------------------------------------------------------------------------------

var galaxyOnce struct {
	sync.Once
	data []byte
	etag string
}

// GalaxyCloud returns the encoded point cloud and its ETag, generating it on first use.
//
// Memoised rather than cached on disk: it is under two megabytes, it takes a fraction of a second to
// build, and it depends on nothing outside this package — so a process restart rebuilding it is
// cheaper than the bookkeeping of a file that could go stale.
func GalaxyCloud() (data []byte, etag string) {
	galaxyOnce.Do(func() {
		galaxyOnce.data = buildGalaxyCloud(GalaxyPoints, galaxySeed)
		galaxyOnce.etag = fmt.Sprintf(`W/"galaxy-%d-%d-%d"`, GalaxyVersion, GalaxyPoints, galaxySeed)
	})
	return galaxyOnce.data, galaxyOnce.etag
}

// buildGalaxyCloud samples n points and encodes them.
func buildGalaxyCloud(n int, seed uint64) []byte {
	out := make([]byte, galaxyHeaderSize+n*galaxyRecordSize)
	copy(out, galaxyMagic)
	binary.LittleEndian.PutUint16(out[8:], GalaxyVersion)
	binary.LittleEndian.PutUint16(out[10:], galaxyRecordSize)
	binary.LittleEndian.PutUint32(out[12:], uint32(n))
	binary.LittleEndian.PutUint32(out[16:], math.Float32bits(galaxyPcPerUnit))
	binary.LittleEndian.PutUint32(out[20:], uint32(seed))

	g := newGalaxyRNG(seed)
	palette := bvPalette()
	pops := galaxyPopulations()
	off := galaxyHeaderSize
	for i, p := range pops {
		// The last population takes whatever is left, so the record count is exactly n however the
		// shares round.
		count := int(math.Round(p.share * float64(n)))
		if i == len(pops)-1 {
			count = (len(out) - off) / galaxyRecordSize
		}
		for k := 0; k < count && off+galaxyRecordSize <= len(out); k++ {
			x, y, z := g.point(p)
			r, gg, b := p.colour(g, palette)
			putGalaxyPoint(out[off:], x, y, z, r, gg, b, uint8(math.Round(255*p.lum)))
			off += galaxyRecordSize
		}
	}
	return out[:off]
}

// point draws one accepted position from a population, in heliocentric galactic PARSECS.
//
// The Sun's neighbourhood is carved out here rather than in each sampler: it is a property of the
// scene the cloud is drawn into, not of any one component.
func (g *galaxyRNG) point(p population) (x, y, z float64) {
	for tries := 0; tries < 4096; tries++ {
		r, beta, zk, ok := p.sample(g)
		if !ok {
			continue
		}
		hx, hy, hz := galactocentricToHeliocentric(r, beta, zk)
		x, y, z = hx*1000, hy*1000, hz*1000
		if math.Sqrt(x*x+y*y+z*z) < sunCarveOutPc {
			continue
		}
		if math.Abs(x) < galaxyMaxPc && math.Abs(y) < galaxyMaxPc && math.Abs(z) < galaxyMaxPc {
			return x, y, z
		}
	}
	// Unreachable for every population above — each accepts within a handful of tries — but a sampler
	// added later that never accepts must not spin forever. The Sun's own position is the one place
	// guaranteed to be inside every bound.
	return 0, 0, 0
}

// galaxyMaxPc is what an int16 position can address. A point beyond it would wrap to the far side of
// the Galaxy, so it is dropped instead.
const galaxyMaxPc = 32767 * galaxyPcPerUnit

// colour is the population's hue for one point: its blackbody colour at a sampled B−V, or its own
// fixed emission colour when it is not a blackbody at all.
func (p population) colour(g *galaxyRNG, palette [][3]uint8) (uint8, uint8, uint8) {
	if p.rgb != nil {
		return p.rgb[0], p.rgb[1], p.rgb[2]
	}
	bv := p.bvMean + p.bvSigma*g.normal()
	return paletteAt(palette, bv)
}

// putGalaxyPoint writes one record, quantising the position and clamping rather than wrapping.
func putGalaxyPoint(rec []byte, xPc, yPc, zPc float64, r, g, b, lum uint8) {
	put := func(at int, v float64) {
		q := math.Round(v / galaxyPcPerUnit)
		binary.LittleEndian.PutUint16(rec[at:], uint16(int16(clampF(q, -32767, 32767))))
	}
	put(0, xPc)
	put(2, yPc)
	put(4, zPc)
	rec[6], rec[7], rec[8], rec[9] = r, g, b, lum
}

func clampF(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

// --- the colour ramp -----------------------------------------------------------------------------

// bvPaletteSteps is how finely the blackbody ramp is sampled. blackbodyRGB integrates a Planck
// spectrum over eighty wavelengths, which is far too slow to run per point — and pointless, since the
// step here is a tenth of the narrowest population's colour spread.
const bvPaletteSteps = 96

// bvPalette is the blackbody colour across the range Ballesteros' relation is valid over.
func bvPalette() [][3]uint8 {
	out := make([][3]uint8, bvPaletteSteps)
	for i := range out {
		bv := bvFitMin + (bvFitMax-bvFitMin)*float64(i)/float64(bvPaletteSteps-1)
		if t, ok := bvToTemperatureK(bv); ok {
			r, g, b := blackbodyRGB(t)
			out[i] = [3]uint8{r, g, b}
			continue
		}
		out[i] = [3]uint8{255, 255, 255}
	}
	return out
}

// paletteAt looks up a B−V, clamping to the ends of the fit rather than extrapolating past them.
func paletteAt(palette [][3]uint8, bv float64) (uint8, uint8, uint8) {
	if len(palette) == 0 {
		return 255, 255, 255
	}
	t := (clampF(bv, bvFitMin, bvFitMax) - bvFitMin) / (bvFitMax - bvFitMin)
	i := int(math.Round(t * float64(len(palette)-1)))
	c := palette[i]
	return c[0], c[1], c[2]
}

// --- the generator -------------------------------------------------------------------------------

// galaxyRNG is splitmix64, hand-rolled on purpose: the cloud is served with an ETag, so its bytes
// must not change when the standard library's generator does.
type galaxyRNG struct {
	state    uint64
	spare    float64
	hasSpare bool
}

func newGalaxyRNG(seed uint64) *galaxyRNG {
	return &galaxyRNG{state: seed}
}

func (g *galaxyRNG) next() uint64 {
	g.state += 0x9e3779b97f4a7c15
	z := g.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// float is uniform on [0, 1).
func (g *galaxyRNG) float() float64 {
	return float64(g.next()>>11) / (1 << 53)
}

// unit is uniform on (0, 1) — the open interval the log and atanh draws need, since both diverge at
// an endpoint.
func (g *galaxyRNG) unit() float64 {
	return clampF(g.float(), 1e-9, 1-1e-9)
}

// normal is a standard normal, Box–Muller with the second value kept rather than thrown away.
func (g *galaxyRNG) normal() float64 {
	if g.hasSpare {
		g.hasSpare = false
		return g.spare
	}
	r := math.Sqrt(-2 * math.Log(g.unit()))
	theta := 2 * math.Pi * g.float()
	g.spare, g.hasSpare = r*math.Sin(theta), true
	return r * math.Cos(theta)
}
