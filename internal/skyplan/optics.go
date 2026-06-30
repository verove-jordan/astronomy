// Package skyplan turns the deep-sky catalog into a ranked list of "what's good to image tonight",
// combining the astro ephemeris primitives with the observer's location, time and optical setup. It
// depends only on internal/astro, internal/skycat and the standard library.
package skyplan

// arcsecPerRadian is the small-angle constant (≈ 206265 arcseconds per radian).
const arcsecPerRadian = 206264.806

// Optics describes the telescope + camera: focal length and aperture (mm), pixel pitch (µm), sensor
// dimensions (px), and an optional Barlow factor. Image scale and field of view derive from these.
type Optics struct {
	FocalMM    float64
	ApertureMM float64
	PixelUm    float64
	SensorWpx  int
	SensorHpx  int
	BarlowX    float64 // optical amplifier on the focal length; ≤0 means none (×1)
}

// EffectiveFocalMM is the focal length presented to the camera or eyepiece after the Barlow (if any):
// a Barlow of factor BarlowX multiplies it. A BarlowX of ≤0 means "no Barlow" (×1). A value below 1
// (a focal reducer) is honored too.
func (o Optics) EffectiveFocalMM() float64 {
	if o.BarlowX > 0 {
		return o.FocalMM * o.BarlowX
	}
	return o.FocalMM
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

// FRatio returns the effective focal ratio (effective focal length / aperture), so a Barlow raises it.
func (o Optics) FRatio() float64 {
	if o.ApertureMM <= 0 {
		return 0
	}
	return o.EffectiveFocalMM() / o.ApertureMM
}
