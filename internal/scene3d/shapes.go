package scene3d

import (
	_ "embed"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/verove-jordan/astronomy/internal/annotate"
	"github.com/verove-jordan/astronomy/internal/skycat"
)

// Giving an object a shape instead of a flat card.
//
// A billboard is honest but wrong in an obvious way: a galaxy is not a sticker facing the camera. The
// question is how much real information exists to do better, and the answer differs sharply by object
// class — so this file keeps three tiers apart and labels every shape with which one it came from.
// Nothing here invents a shape and presents it as a measurement.
//
//   - ShapeMeasured — the geometry follows from catalogued numbers. A spiral galaxy is a thin disc,
//     so its projected axis ratio gives a real inclination; a planetary nebula's radius is its angular
//     size times its distance.
//   - ShapeAssumed — the FORM is a standard assumption even though its size is measured: that a
//     planetary nebula is a shell, or that an elliptical is oblate rather than triaxial.
//   - ShapeModelled — no measurement of the third dimension exists at all. Either a published
//     qualitative structure, hand-encoded with a citation, or the generic brightness inversion.
type ShapeSource string

const (
	ShapeMeasured ShapeSource = "measured"
	ShapeAssumed  ShapeSource = "assumed"
	ShapeModelled ShapeSource = "modelled"
)

// Shape kinds the viewer knows how to tessellate. Anything else falls back to the flat plane, which
// is always correct-looking at depth zero and never claims more than the image itself.
const (
	ShapePlane  = "plane"
	ShapeDisc   = "disc"
	ShapeShell  = "shell"
	ShapeVolume = "volume"
)

// Shape is how one object occupies space. The viewer tessellates it with no astronomy of its own —
// every decision (which kind, which tier, the angles, the radius, the caveat) is made here, where the
// catalogue is.
type Shape struct {
	Kind   string      `json:"kind"`
	Source ShapeSource `json:"source"`
	// Note states, in one line, where the geometry came from and what it assumes. It is shown in the
	// UI verbatim, because a shape the viewer cannot question is a shape the viewer will believe.
	Note string `json:"note"`
	// Cite names the published structure a curated shape follows. Empty for everything else.
	Cite string `json:"cite,omitempty"`

	// InclinationDeg is the tilt of a disc from face-on (0 = face-on, 90 = edge-on); PositionAngleDeg
	// orients its major axis in the image. FlipAmbiguous is set whenever the near and far edges cannot
	// be told apart — which, from an ellipse alone, is always.
	InclinationDeg   float64 `json:"inclination_deg,omitempty"`
	PositionAngleDeg float64 `json:"position_angle_deg,omitempty"`
	FlipAmbiguous    bool    `json:"flip_ambiguous,omitempty"`

	// RadiusPc is the object's physical semi-major extent; ThicknessPc is the disc's scale height or
	// the shell's wall thickness. Both follow from the angular size and the distance.
	RadiusPc    float64 `json:"radius_pc,omitempty"`
	ThicknessPc float64 `json:"thickness_pc,omitempty"`

	// Profile carries the free parameters of a volume: how deep it is relative to its width, how
	// sharply brightness maps to depth, and for a curated shape the parameters of that structure.
	Profile *VolumeProfile `json:"profile,omitempty"`
}

// VolumeProfile parameterises a diffuse nebula's third dimension.
//
// The generic case rests on one assumption, stated plainly: for optically thin gas the image records
// ∫ε·dz along each line of sight, which is one number where a function is wanted. Assuming the
// structure is as deep as it is wide makes the inversion determinate — depth ∝ brightness^Exponent —
// and it is the only thing standing between the image and a shape. It is a MODEL. It is not measured,
// it has no error bar, and for a nebula that is known not to be isotropic it will be wrong in a way
// that still looks convincing.
type VolumeProfile struct {
	// DepthPc is the total extent along the line of sight at the brightest pixel.
	DepthPc float64 `json:"depth_pc"`
	// Exponent maps brightness to depth. 0.5 is the isotropic-blob value: a spherical clump of
	// brightness I has a depth proportional to √I.
	Exponent float64 `json:"exponent"`
	// Bowl bends the volume into a blister — a cavity opening toward the observer, as in M42, where
	// the nebula is not a cloud but the excavated near face of one. 0 = a symmetric cloud, 1 = a full
	// bowl. Only ever set by a curated entry.
	Bowl float64 `json:"bowl,omitempty"`
	// Hollow carves out the middle, for a shell or torus seen face-on. 0 = filled.
	Hollow float64 `json:"hollow,omitempty"`
}

// extentAngleDeg is the object's major-axis angle in IMAGE space, in degrees. Deliberately taken
// from Extent — the footprint annotate already projected — rather than from the catalogued sky
// position angle: the footprint is what the quad is cut by, and re-deriving the orientation from the
// sky would let the shape and its own texture rotate apart.
func extentAngleDeg(l annotate.Label) float64 {
	if l.Extent == nil {
		return 0
	}
	return l.Extent.AngleRad * 180 / math.Pi
}

// discQ0 is the intrinsic axial ratio of a spiral disc — how thick it is compared to how wide. The
// classical Hubble value is 0.2; later types run thinner and earlier ones thicker, but the
// inclination is barely sensitive to it except near face-on, where it is degenerate anyway.
const discQ0 = 0.2

// arcminToPc converts an angular extent to a physical one at a given distance. Small-angle: an
// arcminute is 1/3437.75 radian, and at these angular scales the exact tangent differs in the sixth
// decimal.
func arcminToPc(arcmin, distPc float64) float64 {
	return arcmin / 60 * math.Pi / 180 * distPc
}

// discInclination solves the Hubble relation cos²i = (q² − q₀²)/(1 − q₀²) for a disc's tilt, given
// its projected axis ratio q = b/a.
//
// It is well behaved between roughly 50° and 80°, where it is good to a few degrees. Below q₀ the
// equation has no solution — a disc cannot look flatter than it is — and that is reported rather than
// clamped, because a galaxy measured flatter than any disc is telling you the measurement is wrong or
// the object is not a disc.
func discInclination(q float64) (float64, bool) {
	if !(q > 0) || q > 1 {
		return 0, false
	}
	num := q*q - discQ0*discQ0
	if num < 0 {
		return 0, false
	}
	c := math.Sqrt(num / (1 - discQ0*discQ0))
	return math.Acos(math.Min(1, c)) * 180 / math.Pi, true
}

// isDiscGalaxy reads the Hubble class. Spirals and lenticulars are discs, so their projection carries
// a real inclination; ellipticals are triaxial spheroids whose projected shape determines nothing
// about the third dimension, and they are handled separately and said so.
func isDiscGalaxy(morphology string) bool {
	m := strings.TrimSpace(morphology)
	if m == "" {
		return false
	}
	return m[0] == 'S' || m[0] == 'I' // S, SA, SB, S0, Sd… and irregulars, which are at least flattened
}

// shapeFor decides how one catalogued object occupies space. distPc is the distance already settled
// by the caller (measured from the frame for a cluster, or looked up).
func shapeFor(l annotate.Label, distPc float64) *Shape {
	if distPc <= 0 || l.Extent == nil {
		return nil
	}
	if s := curatedShape(l, distPc); s != nil {
		return s
	}
	switch l.Type {
	case "galaxy":
		return galaxyShape(l, distPc)
	case "planetary_nebula", "supernova_remnant":
		return shellShape(l, distPc)
	case "emission_nebula", "reflection_nebula", "dark_nebula", "nebula":
		return volumeShape(l, distPc)
	}
	return nil // clusters and everything else keep the plane: a cluster IS its stars, already in 3D
}

// galaxyShape turns a galaxy's projected ellipse into a tilted disc.
func galaxyShape(l annotate.Label, distPc float64) *Shape {
	if l.Diameter <= 0 || l.MinorAxis <= 0 {
		return nil
	}
	q := l.MinorAxis / l.Diameter
	radius := arcminToPc(l.Diameter/2, distPc)

	if !isDiscGalaxy(l.Morphology) {
		// An elliptical's projection is genuinely ambiguous: a face-on oblate and an edge-on prolate
		// spheroid can look identical. The projected ratio is therefore only a LOWER bound on the
		// flattening, and the shape is drawn as the roundest object consistent with the image.
		return &Shape{
			Kind: ShapeDisc, Source: ShapeAssumed,
			InclinationDeg: 0, PositionAngleDeg: extentAngleDeg(l), FlipAmbiguous: true,
			RadiusPc: radius, ThicknessPc: radius * q,
			Note: fmt.Sprintf("oblate spheroid at the projected axis ratio b/a = %.2f; a spheroid's projection does not fix its 3D shape, so this flattening is a lower bound", q),
		}
	}
	inc, ok := discInclination(q)
	if !ok {
		return nil
	}
	return &Shape{
		Kind: ShapeDisc, Source: ShapeMeasured,
		InclinationDeg: inc, PositionAngleDeg: extentAngleDeg(l), FlipAmbiguous: true,
		RadiusPc: radius, ThicknessPc: radius * discQ0,
		Note: fmt.Sprintf("inclination %.0f° from the catalogued axis ratio b/a = %.2f (thin-disc relation, q0 = %.1f); which edge tilts toward us cannot be told from an ellipse", inc, q, discQ0),
	}
}

// maxShellAxisRatio gates the shell assumption. A round or mildly elliptical planetary nebula really
// is a shell to a good approximation; a strongly elongated one is bipolar, and forcing an ellipsoid
// on it would be a worse answer than the honest flat plane.
const maxShellAxisRatio = 0.55

// shellShape models an expanding shell — a planetary nebula or a supernova remnant.
func shellShape(l annotate.Label, distPc float64) *Shape {
	if l.Diameter <= 0 {
		return nil
	}
	q := 1.0
	if l.MinorAxis > 0 {
		q = l.MinorAxis / l.Diameter
	}
	if q < maxShellAxisRatio {
		return nil // bipolar, not a shell — the plane is the more honest answer
	}
	radius := arcminToPc(l.Diameter/2, distPc)
	return &Shape{
		Kind: ShapeShell, Source: ShapeAssumed,
		PositionAngleDeg: extentAngleDeg(l),
		RadiusPc:         radius,
		// A planetary nebula's bright rim is a thin wall; a quarter of the radius is the usual look.
		ThicknessPc: radius * 0.25,
		Note: fmt.Sprintf("expanding shell, radius %.2f pc from the catalogued %.1f′ at %.0f pc; the size is measured, the shell form is assumed",
			radius, l.Diameter, distPc),
	}
}

// volumeShape is the generic diffuse-nebula model: give the image a third dimension by assuming the
// structure is roughly as deep as it is wide.
func volumeShape(l annotate.Label, distPc float64) *Shape {
	if l.Diameter <= 0 {
		return nil
	}
	width := arcminToPc(l.Diameter, distPc)
	return &Shape{
		Kind: ShapeVolume, Source: ShapeModelled,
		PositionAngleDeg: extentAngleDeg(l),
		RadiusPc:         width / 2,
		Profile:          &VolumeProfile{DepthPc: width * 0.6, Exponent: 0.5},
		Note:             "modelled: depth from brightness assuming the nebula is about as deep as it is wide (optically thin, depth ∝ √I). No measurement of its third dimension exists",
	}
}

// --- curated shapes --------------------------------------------------------------------------------

// shapes.csv holds the objects whose three-dimensional structure has actually been published, as
// parameters plus a citation. It is short by necessity: for most nebulae nobody knows, and guessing
// object by object would be worse than one stated generic assumption.
//
//go:embed shapes.csv
var curatedCSV string

type curatedEntry struct {
	kind     string
	bowl     float64
	hollow   float64
	depthRel float64 // depth along the line of sight, as a fraction of the object's width
	exponent float64
	cite     string
	note     string
}

var curatedShapes = sync.OnceValue(loadCurated)

func loadCurated() map[string]curatedEntry {
	out := map[string]curatedEntry{}
	r := csv.NewReader(strings.NewReader(curatedCSV))
	r.FieldsPerRecord = 8
	if _, err := r.Read(); err != nil {
		return out
	}
	num := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // one malformed row must not cost every other object its shape
		}
		e := curatedEntry{
			kind: strings.TrimSpace(rec[1]), bowl: num(rec[2]), hollow: num(rec[3]),
			depthRel: num(rec[4]), exponent: num(rec[5]),
			cite: strings.TrimSpace(rec[6]), note: strings.TrimSpace(rec[7]),
		}
		if e.exponent <= 0 {
			e.exponent = 0.5
		}
		for _, name := range strings.Split(rec[0], "/") {
			if k := skycat.Normalize(name); k != "" {
				if _, seen := out[k]; !seen {
					out[k] = e
				}
			}
		}
	}
	return out
}

// curatedShape returns the published structure for an object, or nil when nobody has published one.
func curatedShape(l annotate.Label, distPc float64) *Shape {
	table := curatedShapes()
	var e curatedEntry
	var found bool
	for _, n := range []string{l.Name, l.Secondary} {
		if k := skycat.Normalize(n); k != "" {
			if v, ok := table[k]; ok {
				e, found = v, true
				break
			}
		}
	}
	if !found || l.Diameter <= 0 {
		return nil
	}
	width := arcminToPc(l.Diameter, distPc)
	return &Shape{
		Kind: e.kind, Source: ShapeModelled,
		PositionAngleDeg: extentAngleDeg(l),
		RadiusPc:         width / 2,
		Profile: &VolumeProfile{
			DepthPc: width * e.depthRel, Exponent: e.exponent,
			Bowl: e.bowl, Hollow: e.hollow,
		},
		Cite: e.cite,
		Note: "modelled on the published structure: " + e.note,
	}
}
