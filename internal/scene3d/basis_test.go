package scene3d

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

// vecToSky is the inverse of skyToVec, for building test fixtures from a chosen geometry rather
// than from hand-computed coordinates.
func vecToSky(v vec3) (raDeg, decDeg float64) {
	const radDeg = 180 / math.Pi
	ra := math.Atan2(v.Y, v.X) * radDeg
	if ra < 0 {
		ra += 360
	}
	return ra, math.Asin(math.Max(-1, math.Min(1, v.Z/v.length()))) * radDeg
}

// testCamera is a synthetic field: an orthonormal image triad plus its half-field tangents. Every
// geometry test builds one, turns it into the annotate.Frame the engine would have shipped, and
// checks that newBasis gets the camera back.
type testCamera struct {
	x, y, z            vec3
	tanHalfW, tanHalfH float64
	wf, hf             int
}

// frame renders the camera as the three sky positions annotate anchors a final image with.
func (c testCamera) frame() annotate.Frame {
	var f annotate.Frame
	f.CenterRA, f.CenterDec = vecToSky(c.z)
	ex, _ := vec3{
		c.z.X + c.tanHalfW*c.x.X,
		c.z.Y + c.tanHalfW*c.x.Y,
		c.z.Z + c.tanHalfW*c.x.Z,
	}.unit()
	ey, _ := vec3{
		c.z.X + c.tanHalfH*c.y.X,
		c.z.Y + c.tanHalfH*c.y.Y,
		c.z.Z + c.tanHalfH*c.y.Z,
	}.unit()
	f.XEdgeRA, f.XEdgeDec = vecToSky(ex)
	f.YEdgeRA, f.YEdgeDec = vecToSky(ey)
	return f
}

// starAt returns the sky position a star would have if it sat at final-image pixel (px, py).
func (c testCamera) starAt(px, py float64) (raDeg, decDeg float64) {
	u := (px - float64(c.wf-1)/2) / (float64(c.wf-1) / 2) * c.tanHalfW
	v := (py - float64(c.hf-1)/2) / (float64(c.hf-1) / 2) * c.tanHalfH
	d, _ := vec3{
		c.z.X + u*c.x.X + v*c.y.X,
		c.z.Y + u*c.x.Y + v*c.y.Y,
		c.z.Z + u*c.x.Z + v*c.y.Z,
	}.unit()
	return vecToSky(d)
}

// rotatedCamera builds a camera looking at (ra0, dec0) with the image rolled by rollDeg and
// optionally mirrored — the star-diagonal parity the engine sees on some sessions.
func rotatedCamera(ra0, dec0, rollDeg float64, mirrored bool, wf, hf int) testCamera {
	z := skyToVec(ra0, dec0)
	// East and North at the field centre, the natural frame to roll within.
	east, _ := vec3{-z.Y, z.X, 0}.unit()
	north := vec3{
		z.Y*east.Z - z.Z*east.Y,
		z.Z*east.X - z.X*east.Z,
		z.X*east.Y - z.Y*east.X,
	}
	sin, cos := math.Sincos(rollDeg * math.Pi / 180)
	x, _ := vec3{
		east.X*cos + north.X*sin,
		east.Y*cos + north.Y*sin,
		east.Z*cos + north.Z*sin,
	}.unit()
	if mirrored {
		x = x.scale(-1)
	}
	y, _ := vec3{
		-east.X*sin + north.X*cos,
		-east.Y*sin + north.Y*cos,
		-east.Z*sin + north.Z*cos,
	}.unit()
	return testCamera{x: x, y: y, z: z, tanHalfW: 0.013, tanHalfH: 0.0098, wf: wf, hf: hf}
}

func TestNewBasis_RecoversTheCamera(t *testing.T) {
	tests := []struct {
		name          string
		ra, dec, roll float64
		mirrored      bool
	}{
		{"orion, unrolled", 83.82, -5.39, 0, false},
		{"rolled 37 degrees", 83.82, -5.39, 37, false},
		{"mirrored field (star diagonal)", 148.9, 69.07, 12, true},
		{"near the pole", 37.95, 89.26, 100, false},
		{"across the RA wrap", 0.5, 41.27, -25, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cam := rotatedCamera(tt.ra, tt.dec, tt.roll, tt.mirrored, 3720, 2790)
			b, err := newBasis(cam.frame())
			require.NoError(t, err)

			assert.InDelta(t, cam.tanHalfW, b.TanHalfW, 1e-9, "half-width tangent")
			assert.InDelta(t, cam.tanHalfH, b.TanHalfH, 1e-9, "half-height tangent")
			// The recovered axes must be the camera's own, not merely some orthonormal triad.
			assert.InDelta(t, 1, cam.x.dot(b.X), 1e-9, "image x axis")
			assert.InDelta(t, 1, cam.y.dot(b.Y), 1e-9, "image y axis")
			assert.InDelta(t, 1, cam.z.dot(b.Z), 1e-9, "optical axis")
			assert.Equal(t, !tt.mirrored, b.RightHanded, "parity")
		})
	}
}

// TestBasis_ReproducesThePhotograph is the property the whole feature rests on: with the depth
// slider at zero every star sits on one plane, and the perspective projection of that plane must be
// the photograph itself. Here that means projecting a star back through the basis has to return the
// pixel it was detected at — otherwise the 3D view opens from a picture that is not the run's.
func TestBasis_ReproducesThePhotograph(t *testing.T) {
	const wf, hf = 3720, 2790
	cam := rotatedCamera(83.82, -5.39, 37, false, wf, hf)
	b, err := newBasis(cam.frame())
	require.NoError(t, err)

	for _, px := range []float64{0, 137.5, float64(wf-1) / 2, 3000, wf - 1} {
		for _, py := range []float64{0, 42, float64(hf-1) / 2, hf - 1} {
			ra, dec := cam.starAt(px, py)
			d := b.project(ra, dec)
			require.Greater(t, d.Z, 0.0, "the field must be in front of the observer")

			// The screen position a pinhole camera puts this direction at, mapped back to pixels.
			gotX := (d.X/d.Z/b.TanHalfW)*(float64(wf-1)/2) + float64(wf-1)/2
			gotY := (d.Y/d.Z/b.TanHalfH)*(float64(hf-1)/2) + float64(hf-1)/2
			assert.InDelta(t, px, gotX, 1e-6, "x at (%.1f, %.1f)", px, py)
			assert.InDelta(t, py, gotY, 1e-6, "y at (%.1f, %.1f)", px, py)
		}
	}
}

func TestNewBasis_RejectsDegenerateGeometry(t *testing.T) {
	// All three anchors on the same point: no image axes can be derived from it.
	f := annotate.Frame{
		CenterRA: 10, CenterDec: 20,
		XEdgeRA: 10, XEdgeDec: 20,
		YEdgeRA: 10, YEdgeDec: 20,
	}
	_, err := newBasis(f)
	require.Error(t, err)
}
