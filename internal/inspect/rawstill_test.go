package inspect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestClassifyRawStills_Tokens classifies phone raws by folder name alone (no sips/EXIF needed): a
// darks/, offset/ or flats/ subfolder is authoritative; a loose file defaults to a light.
func TestClassifyRawStills_Tokens(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	darkP := mk("darks/IMG_1.dng")
	biasP := mk("offset/IMG_2.dng")
	flatP := mk("flats/IMG_3.dng")
	lightP := mk("IMG_4.dng")

	got := map[string]FrameType{}
	for _, f := range ClassifyRawStills(context.Background(), []string{darkP, biasP, flatP, lightP}) {
		got[f.Path] = f.Type
		if isCalibration(f.Type) && f.Filter != "" {
			t.Errorf("%s: calibration frame should carry no filter, got %q", f.Path, f.Filter)
		}
	}
	for path, want := range map[string]FrameType{darkP: Dark, biasP: Bias, flatP: Flat, lightP: Light} {
		if got[path] != want {
			t.Errorf("%s classified as %s, want %s", filepath.Base(path), got[path], want)
		}
	}
}

// TestSetKeyFor_ISO: ISO splits phone light sets, but a FITS frame (ISO 0) keeps its historical key.
func TestSetKeyFor_ISO(t *testing.T) {
	a := setKeyFor(&Frame{Type: Light, Filter: "RGB", ISO: 800}, false)
	b := setKeyFor(&Frame{Type: Light, Filter: "RGB", ISO: 3200}, false)
	if a == b {
		t.Fatal("lights differing only by ISO should have different keys")
	}
	fitsKey := setKeyFor(&Frame{Type: Dark, ExposureMs: 300000, Gain: 200, Offset: 50, BinX: 1}, false)
	if fitsKey.ISO != 0 {
		t.Fatalf("FITS dark key ISO = %d, want 0", fitsKey.ISO)
	}
}
