package scene3d

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

// Where a star is going, in three dimensions.
//
// The catalogue measures motion in two unrelated ways: proper motion, which is an ANGLE per year
// across the sky, and radial velocity, which is a SPEED along the line of sight. Neither is a
// velocity on its own — an angle per year only becomes km/s once the distance is known, which is
// exactly what this scene has. Combining the three gives a real space velocity, and the viewer draws
// it as "where this star will be in N years", so the arrow's length is proportional to speed and also
// means something.

// kmsPerMasYrPc converts proper motion to tangential velocity: v = 4.74047 · μ(″/yr) · d(pc) km/s.
// Proper motion arrives in MILLIarcseconds per year, so the constant carries the extra 1/1000.
const kmsPerMasYrPc = 4.74047 / 1000

// maxSpeedKmS rejects a velocity no star has. The galaxy's escape speed near the Sun is ~550 km/s and
// the fastest known hypervelocity stars reach ~1700; past this the inputs are wrong (a bad parallax
// inflates tangential velocity without limit, since v scales with distance).
const maxSpeedKmS = 3000

// spaceVelocity returns a star's velocity in SCENE coordinates, km/s. ok is false when the catalogue
// measured nothing usable — no distance, or neither a proper motion nor a radial velocity.
//
// The tangential part is decomposed on the local East/North axes at the star's own position, because
// that is the frame proper motion is quoted in; the radial part rides the line of sight. All three
// are then rotated into the scene basis with the same projection the star's position used.
func spaceVelocity(info *annotate.StarInfo, b basis) (vec3, bool) {
	if info == nil || info.DistPc <= 0 {
		return vec3{}, false
	}
	hasPM := info.PMRA != 0 || info.PMDec != 0
	hasRV := info.RVKmS != nil
	if !hasPM && !hasRV {
		return vec3{}, false
	}

	const degRad = math.Pi / 180
	sinRA, cosRA := math.Sincos(info.RADeg * degRad)
	sinDec, cosDec := math.Sincos(info.DecDeg * degRad)

	// The local triad at the star: outward along the line of sight, East (increasing RA) and North
	// (increasing Dec). PMRA already carries the cos δ factor, so East needs no further scaling.
	radial := vec3{cosDec * cosRA, cosDec * sinRA, sinDec}
	east := vec3{-sinRA, cosRA, 0}
	north := vec3{-sinDec * cosRA, -sinDec * sinRA, cosDec}

	vEast := kmsPerMasYrPc * info.PMRA * info.DistPc
	vNorth := kmsPerMasYrPc * info.PMDec * info.DistPc
	vRadial := 0.0
	if hasRV {
		vRadial = *info.RVKmS
	}

	v := vec3{
		east.X*vEast + north.X*vNorth + radial.X*vRadial,
		east.Y*vEast + north.Y*vNorth + radial.Y*vRadial,
		east.Z*vEast + north.Z*vNorth + radial.Z*vRadial,
	}
	if speed := v.length(); !(speed > 0) || speed > maxSpeedKmS || math.IsNaN(speed) {
		return vec3{}, false
	}
	// Into scene coordinates, through the same basis the position went through — so an arrow can
	// never point somewhere the star is not.
	return vec3{v.dot(b.X), v.dot(b.Y), v.dot(b.Z)}, true
}
