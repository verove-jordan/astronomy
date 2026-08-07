// Package skyplan turns the deep-sky catalog into a ranked list of "what's good to image tonight",
// combining the astro ephemeris primitives with the observer's location, time and optical setup. It
// depends only on internal/astro, internal/skycat and the standard library.
package skyplan

// arcsecPerRadian is the small-angle constant (≈ 206265 arcseconds per radian).
const arcsecPerRadian = 206264.806

// Optics describes the telescope + camera: focal length and aperture (mm), pixel pitch (µm), sensor
// dimensions (px), and the optional focal multipliers (Barlow, reducer). Image scale and field of view
// derive from these. JSON names match the /api/sky equipment echo — mosaic plans persist an Optics
// snapshot.
type Optics struct {
	FocalMM    float64 `json:"focal_mm"`
	ApertureMM float64 `json:"aperture_mm"`
	PixelUm    float64 `json:"pixel_um"`
	SensorWpx  int     `json:"sensor_w_px"`
	SensorHpx  int     `json:"sensor_h_px"`
	BarlowX    float64 `json:"barlow_x"`  // optical amplifier on the focal length; ≤0 means none (×1)
	ReducerX   float64 `json:"reducer_x"` // focal reducer, e.g. 0.66; ≤0 means none (×1)
}

// EffectiveFocalMM is the focal length presented to the camera or eyepiece once the imaging train's
// multipliers are folded in. Barlow and reducer are INDEPENDENT because they play different roles: a
// reducer usually lives permanently in the train while a Barlow is swapped in for planets, so a rig can
// carry both (740 × 2 × 0.66 = 977 mm). Either at ≤0 means "not fitted" (×1), which is what an unset
// field decodes to — so an Optics that predates the reducer keeps its exact focal length.
func (o Optics) EffectiveFocalMM() float64 {
	f := o.FocalMM
	if o.BarlowX > 0 {
		f *= o.BarlowX
	}
	if o.ReducerX > 0 {
		f *= o.ReducerX
	}
	return f
}

// ImageScale returns the image scale in arcseconds per pixel (206.265 × pixel_µm / effective_focal_mm).
func (o Optics) ImageScale() float64 {
	f := o.EffectiveFocalMM()
	if f <= 0 {
		return 0
	}
	return o.PixelUm / 1000 / f * arcsecPerRadian
}

// FOV returns the field of view (width, height) in degrees.
func (o Optics) FOV() (wDeg, hDeg float64) {
	scale := o.ImageScale()
	return scale * float64(o.SensorWpx) / 3600, scale * float64(o.SensorHpx) / 3600
}

// FOVMinArcmin returns the smaller field-of-view dimension in arcminutes (the framing constraint).
func (o Optics) FOVMinArcmin() float64 {
	w, h := o.FOV()
	min := w
	if h < min {
		min = h
	}
	return min * 60
}

// FRatio returns the effective focal ratio (effective focal length / aperture), so a Barlow raises it
// and a reducer lowers it.
func (o Optics) FRatio() float64 {
	if o.ApertureMM <= 0 {
		return 0
	}
	return o.EffectiveFocalMM() / o.ApertureMM
}
