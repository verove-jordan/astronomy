package skypano

// canvas.go is the output side: a sky projection wide enough to hold a Milky Way arch.
//
// internal/mosaic could not be reused for this. It projects onto a gnomonic (TAN) tangent plane,
// which is undefined 90 degrees from its centre and grotesque well before that — a 180-degree arc is
// not distorted in TAN, it is unrepresentable — and it assembles one mono plane at a time. So the
// canvas is new, and it is deliberately a small set of projections chosen for what they are good at:
//
//   Stereographic  conformal, so star fields keep their shape; the natural "look up at the whole
//                  sky" rendering, good out to most of a hemisphere.
//   Equirectangular  longitude and latitude straight onto x and y; in GALACTIC coordinates it lays
//                  the Milky Way out as a level band, which is the classic panorama of it.

import "math"

// Projection selects how the sphere is laid onto the canvas.
type Projection int

const (
	// Stereographic preserves shapes. Radius on the canvas is 2*tan(theta/2).
	Stereographic Projection = iota
	// Equirectangular maps longitude and latitude linearly.
	Equirectangular
)

// Frame selects the coordinate system a canvas is built in.
type Frame int

const (
	// Equatorial is RA and declination.
	Equatorial Frame = iota
	// Galactic puts the plane of the Milky Way along the canvas's horizontal.
	Galactic
	// Horizon is azimuth and altitude as the sky stood over the site at one named instant — the frame
	// the arch panorama is drawn in. It needs SiteLatDeg and LSTDeg to be set. See horizon.go.
	Horizon
)

// Canvas describes the output grid.
type Canvas struct {
	Proj Projection
	Fr   Frame
	W, H int
	// Lon0, Lat0 are the projection centre in the canvas's own frame, degrees.
	Lon0, Lat0 float64
	// ScaleDegPerPix is the angular scale at the centre.
	ScaleDegPerPix float64
	// SiteLatDeg and LSTDeg place the horizon: the observer's latitude, and the local sidereal time of
	// the instant the arch is drawn for. Used only when Fr is Horizon.
	SiteLatDeg, LSTDeg float64
}

// projLonLat un-projects a canvas pixel to the canvas's OWN frame — galactic longitude/latitude,
// right ascension/declination, or azimuth/altitude, depending on Fr. ok is false where the pixel
// falls outside the projection.
func (c Canvas) projLonLat(x, y float64) (lon, lat float64, ok bool) {
	// Canvas y runs downward; sky latitude runs up.
	dx := (x - float64(c.W)/2) * c.ScaleDegPerPix
	dy := -(y - float64(c.H)/2) * c.ScaleDegPerPix

	switch c.Proj {
	case Equirectangular:
		lon, lat = c.Lon0+dx, c.Lat0+dy
		if lat > 90 || lat < -90 {
			return 0, 0, false
		}
	default: // Stereographic
		// dx, dy are in degrees at the centre; convert to the projection's radial units.
		r := math.Hypot(dx, dy) * math.Pi / 180
		if r == 0 {
			return c.Lon0, c.Lat0, true
		}
		theta := 2 * math.Atan(r/2)
		if theta >= math.Pi*0.98 {
			return 0, 0, false // essentially the antipode
		}
		// Rotate the centre direction by theta towards the bearing of (dx, dy).
		v := rotateFromCentre(c.Lon0, c.Lat0, theta, math.Atan2(dx, dy))
		lon, lat = vecToLonLat(v)
	}
	return lon, lat, true
}

// AltitudeAt is the altitude in degrees of a canvas pixel, for a Horizon-frame canvas — negative
// below the horizon. ok is false for any other frame, or where the pixel is off the projection.
//
// It exists because the arch is drawn STEREOGRAPHICALLY, so altitude is not a function of the row:
// "is this pixel below the horizon" needs the projection un-done, not a comparison against y.
func (c Canvas) AltitudeAt(x, y float64) (float64, bool) {
	if c.Fr != Horizon {
		return 0, false
	}
	_, lat, ok := c.projLonLat(x, y)
	return lat, ok
}

// PixToSky maps a canvas pixel to a unit vector in the EQUATORIAL frame, which is what panels are
// solved in. ok is false where the pixel falls outside the projection.
func (c Canvas) PixToSky(x, y float64) ([3]float64, bool) {
	lon, lat, ok := c.projLonLat(x, y)
	if !ok {
		return [3]float64{}, false
	}
	if c.Fr == Horizon {
		// lon and lat ARE azimuth and altitude here. Note that a NEGATIVE altitude is still mapped:
		// the landscape under the arch is projected through this same call, and it is entirely below
		// the horizon. What must not be drawn there is the SKY, and that is enforced by clearing the
		// sky's coverage — see the arch assembly — not by refusing the geometry.
		if lat < -90 || lat > 90 {
			return [3]float64{}, false
		}
		return horizonToEquatorial(lon, lat, c.SiteLatDeg, c.LSTDeg), true
	}
	v := lonLatToVec(lon, lat)
	if c.Fr == Galactic {
		v = galacticToEquatorial(v)
	}
	return v, true
}

// SkyToPix is the inverse: an equatorial unit vector to canvas pixels.
func (c Canvas) SkyToPix(v [3]float64) (x, y float64, ok bool) {
	if c.Fr == Galactic {
		v = equatorialToGalactic(v)
	}
	var lon, lat float64
	if c.Fr == Horizon {
		lon, lat = equatorialToHorizon(v, c.SiteLatDeg, c.LSTDeg)
		// And carry the direction itself into the horizon frame. The projection below works on the
		// VECTOR, not on lon/lat, and the Galactic branch above already rebinds v for that reason —
		// leaving the horizon case out meant the stereographic branch dotted a canvas centre expressed
		// in azimuth and altitude against a direction still expressed in right ascension and
		// declination. Two different frames, so every angle came out wrong and panels landed at
		// negative pixel coordinates.
		v = lonLatToVec(lon, lat)
	} else {
		lon, lat = vecToLonLat(v)
	}

	var dx, dy float64
	switch c.Proj {
	case Equirectangular:
		dx = math.Mod(lon-c.Lon0+540, 360) - 180
		dy = lat - c.Lat0
	default:
		centre := lonLatToVec(c.Lon0, c.Lat0)
		cosT := clamp1(dot3(centre, v))
		theta := math.Acos(cosT)
		if theta >= math.Pi*0.98 {
			return 0, 0, false
		}
		r := 2 * math.Tan(theta/2) * 180 / math.Pi
		// Bearing of v as seen from the centre, measured from north through east.
		north := northAt(centre)
		east := cross3(centre, north)
		b := math.Atan2(dot3(v, east), dot3(v, north))
		dx, dy = r*math.Sin(b), r*math.Cos(b)
	}
	return float64(c.W)/2 + dx/c.ScaleDegPerPix, float64(c.H)/2 - dy/c.ScaleDegPerPix, true
}

// rotateFromCentre returns the direction reached by moving theta radians away from (lon0, lat0)
// along the given bearing (radians, from north through east).
func rotateFromCentre(lon0, lat0, theta, bearing float64) [3]float64 {
	c := lonLatToVec(lon0, lat0)
	n := northAt(c)
	e := cross3(c, n)
	dir := [3]float64{
		n[0]*math.Cos(bearing) + e[0]*math.Sin(bearing),
		n[1]*math.Cos(bearing) + e[1]*math.Sin(bearing),
		n[2]*math.Cos(bearing) + e[2]*math.Sin(bearing),
	}
	st, ct := math.Sin(theta), math.Cos(theta)
	return normalize3([3]float64{c[0]*ct + dir[0]*st, c[1]*ct + dir[1]*st, c[2]*ct + dir[2]*st})
}

// northAt is the local "towards +latitude" direction at v.
func northAt(v [3]float64) [3]float64 {
	pole := [3]float64{0, 0, 1}
	n := [3]float64{pole[0] - v[0]*v[2], pole[1] - v[1]*v[2], pole[2] - v[2]*v[2]}
	if dot3(n, n) < 1e-18 {
		return [3]float64{1, 0, 0} // at a pole any direction will do
	}
	return normalize3(n)
}

func lonLatToVec(lon, lat float64) [3]float64 { return RADecToVec(lon, lat) }

func vecToLonLat(v [3]float64) (lon, lat float64) { return VecToRADec(v) }

// Galactic pole and centre, J2000.
var (
	galPole   = RADecToVec(192.85948, 27.12825)
	galCentre = RADecToVec(266.40500, -28.93617)
)

// galacticBasis is the rotation whose rows are the galactic x, y and z axes in equatorial terms.
var galacticBasis = func() [3][3]float64 {
	z := galPole
	x := normalize3([3]float64{
		galCentre[0] - z[0]*dot3(galCentre, z),
		galCentre[1] - z[1]*dot3(galCentre, z),
		galCentre[2] - z[2]*dot3(galCentre, z),
	})
	y := cross3(z, x)
	return [3][3]float64{x, y, z}
}()

func equatorialToGalactic(v [3]float64) [3]float64 {
	return [3]float64{
		dot3(galacticBasis[0], v),
		dot3(galacticBasis[1], v),
		dot3(galacticBasis[2], v),
	}
}

func galacticToEquatorial(v [3]float64) [3]float64 {
	out := [3]float64{}
	for i := 0; i < 3; i++ {
		out[i] = galacticBasis[0][i]*v[0] + galacticBasis[1][i]*v[1] + galacticBasis[2][i]*v[2]
	}
	return out
}
