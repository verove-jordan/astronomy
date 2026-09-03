package calib

import (
	"context"
	"fmt"
)

// PhoneMaster is a persisted phone/DSLR calibration master. Unlike a Master (a Siril-stacked FITS
// applied by `calibrate`), a phone master is built and applied per-pixel in linear light by the
// nightscape recipe, and is keyed by ISO, exposure, sensor dimensions and camera model — the phone
// equivalent of a Master's gain/offset/bin/temperature. It lives in its own library table so the
// deep-sky matcher can never select it.
type PhoneMaster struct {
	Type        MasterType `json:"type"`
	ISO         int64      `json:"iso"`
	ExposureMs  int64      `json:"exposure_ms"`
	CameraModel string     `json:"camera_model,omitempty"`
	Width       int        `json:"width"`
	Height      int        `json:"height"`
	FrameCount  int        `json:"frame_count"`
	Path        string     `json:"path"`
}

// PhoneCalibStore is the persistent phone-calibration-master library (implemented by package store).
type PhoneCalibStore interface {
	ListPhoneMasters(ctx context.Context) ([]PhoneMaster, error)
	SavePhoneMaster(ctx context.Context, m PhoneMaster) error
}

// PhoneKey identifies a phone light set for matching: the sensor (camera model + developed
// dimensions), the ISO, and the exposure (used only for darks).
type PhoneKey struct {
	CameraModel string
	ISO         int64
	ExposureMs  int64
	Width       int
	Height      int
	// ISOInvariant drops ISO from the match. Set it for a raw whose gain is already normalised —
	// a linear DNG such as Apple ProRAW, where the same scene at ISO 2500 and ISO 6400 develops to
	// the SAME pixel level (measured: 4% apart, not the 2.56x the ISO ratio implies).
	//
	// Without it a real session cannot be calibrated at all: a phone on auto-exposure picks a
	// different ISO for almost every frame, so darks shot minutes after the lights — at the same
	// exposure, the same temperature, the same sensor — are refused for differing in a number that
	// no longer describes the sensor state. Exposure still has to match, because dark current
	// genuinely scales with time.
	ISOInvariant bool
}

// PhoneSelection is the phone masters chosen for one light set, plus human-readable notes.
type PhoneSelection struct {
	Dark  *PhoneMaster
	Flat  *PhoneMaster
	Bias  *PhoneMaster
	Notes []string
}

// Any reports whether the selection chose at least one master.
func (s PhoneSelection) Any() bool { return s.Dark != nil || s.Flat != nil || s.Bias != nil }

// MatchPhoneCalibration picks the best dark, flat and bias phone masters for a light set:
//   - dark — same sensor (ISO + dimensions + model) AND same exposure (dark current scales with time);
//   - bias — same sensor (exposure-independent read noise / offset);
//   - flat — same sensor dimensions, most frames wins (vignetting/dust is ISO- and exposure-independent).
//
// Among equal candidates the one built from the most frames wins. When nothing matches, Notes explains
// why rather than failing.
func MatchPhoneCalibration(light PhoneKey, masters []PhoneMaster) PhoneSelection {
	var sel PhoneSelection
	sel.Dark = bestPhoneDark(light, masters)
	sel.Bias = bestPhoneBias(light, masters)
	sel.Flat = bestPhoneFlat(light, masters)
	if !sel.Any() {
		sel.Notes = append(sel.Notes,
			fmt.Sprintf("no phone calibration master for ISO %d %d×%d", light.ISO, light.Width, light.Height))
	}
	return sel
}

func bestPhoneDark(light PhoneKey, masters []PhoneMaster) *PhoneMaster {
	var best *PhoneMaster
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterDark || !sameSensor(light, m) || m.ExposureMs != light.ExposureMs {
			continue
		}
		if best == nil || m.FrameCount > best.FrameCount {
			best = m
		}
	}
	return best
}

func bestPhoneBias(light PhoneKey, masters []PhoneMaster) *PhoneMaster {
	var best *PhoneMaster
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterBias || !sameSensor(light, m) {
			continue
		}
		if best == nil || m.FrameCount > best.FrameCount {
			best = m
		}
	}
	return best
}

// bestPhoneFlat picks the flat with matching sensor dimensions — the vignetting/dust pattern is fixed
// to the optics+sensor, independent of ISO and exposure — preferring the one built from the most frames.
func bestPhoneFlat(light PhoneKey, masters []PhoneMaster) *PhoneMaster {
	var best *PhoneMaster
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterFlat || !sameDimensions(light, m) {
			continue
		}
		if best == nil || m.FrameCount > best.FrameCount {
			best = m
		}
	}
	return best
}

// sameSensor reports whether a master was shot on the same sensor at the same ISO as the light: equal
// ISO and dimensions, and equal camera model when both carry one (an empty model matches, so a master
// with no readable model still applies to the same-dimension, same-ISO light).
func sameSensor(light PhoneKey, m *PhoneMaster) bool {
	if !light.ISOInvariant && m.ISO != light.ISO {
		return false
	}
	if m.CameraModel != "" && light.CameraModel != "" && m.CameraModel != light.CameraModel {
		return false
	}
	return sameDimensions(light, m)
}

func sameDimensions(light PhoneKey, m *PhoneMaster) bool {
	return m.Width == light.Width && m.Height == light.Height
}
