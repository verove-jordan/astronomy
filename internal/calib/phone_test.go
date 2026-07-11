package calib

import "testing"

func TestMatchPhoneCalibration(t *testing.T) {
	const model = "iPhone 15 Pro"
	dark := PhoneMaster{Type: MasterDark, ISO: 3200, ExposureMs: 30000, CameraModel: model, Width: 4032, Height: 3024, FrameCount: 10, Path: "dark.fits"}
	darkOtherExp := PhoneMaster{Type: MasterDark, ISO: 3200, ExposureMs: 15000, CameraModel: model, Width: 4032, Height: 3024, FrameCount: 20, Path: "dark15.fits"}
	bias := PhoneMaster{Type: MasterBias, ISO: 3200, CameraModel: model, Width: 4032, Height: 3024, FrameCount: 30, Path: "bias.fits"}
	flatFew := PhoneMaster{Type: MasterFlat, ISO: 100, Width: 4032, Height: 3024, FrameCount: 5, Path: "flat5.fits"}
	flatMany := PhoneMaster{Type: MasterFlat, ISO: 800, Width: 4032, Height: 3024, FrameCount: 12, Path: "flat12.fits"}
	wrongDims := PhoneMaster{Type: MasterDark, ISO: 3200, ExposureMs: 30000, CameraModel: model, Width: 3024, Height: 4032, FrameCount: 99, Path: "portrait.fits"}
	masters := []PhoneMaster{dark, darkOtherExp, bias, flatFew, flatMany, wrongDims}

	light := PhoneKey{CameraModel: model, ISO: 3200, ExposureMs: 30000, Width: 4032, Height: 3024}
	sel := MatchPhoneCalibration(light, masters)

	if sel.Dark == nil || sel.Dark.Path != "dark.fits" {
		t.Errorf("dark: want dark.fits (matching exposure+dims, not the wrong-dims 99-frame one), got %v", sel.Dark)
	}
	if sel.Bias == nil || sel.Bias.Path != "bias.fits" {
		t.Errorf("bias: want bias.fits, got %v", sel.Bias)
	}
	// Flat ignores ISO/exposure and takes the most frames of a matching dimension.
	if sel.Flat == nil || sel.Flat.Path != "flat12.fits" {
		t.Errorf("flat: want flat12.fits (most frames), got %v", sel.Flat)
	}
}

func TestMatchPhoneCalibration_NoMatch(t *testing.T) {
	masters := []PhoneMaster{{Type: MasterDark, ISO: 800, ExposureMs: 1000, Width: 100, Height: 100, Path: "x"}}
	sel := MatchPhoneCalibration(PhoneKey{ISO: 3200, ExposureMs: 30000, Width: 4032, Height: 3024}, masters)
	if sel.Any() {
		t.Fatalf("expected no match, got %+v", sel)
	}
	if len(sel.Notes) == 0 {
		t.Fatal("expected a note explaining the miss")
	}
}

func TestSameSensor_ModelOptional(t *testing.T) {
	// A master with no camera model still matches a same-ISO/dims light (an empty model matches).
	m := &PhoneMaster{Type: MasterBias, ISO: 3200, Width: 100, Height: 100}
	light := PhoneKey{CameraModel: "iPhone", ISO: 3200, Width: 100, Height: 100}
	if !sameSensor(light, m) {
		t.Fatal("empty master model should match")
	}
	m.CameraModel = "Pixel 8"
	if sameSensor(light, m) {
		t.Fatal("different models should not match")
	}
	m.CameraModel, m.ISO = "iPhone", 800
	if sameSensor(light, m) {
		t.Fatal("different ISO should not match")
	}
}
