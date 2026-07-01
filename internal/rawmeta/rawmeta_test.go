package rawmeta

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestRead_SyntheticTIFF builds a little-endian TIFF/DNG byte buffer with IFD0 (Model, Orientation,
// Exif-pointer) and an Exif sub-IFD (ISO, ExposureTime RATIONAL, pixel dimensions), then asserts Read
// recovers every field. Fully synthetic and pixel-free, so it runs deterministically on any platform
// (no sips/mdls, no real DNG needed).
func TestRead_SyntheticTIFF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frame.dng")
	if err := os.WriteFile(path, buildTIFF(t), 0o600); err != nil {
		t.Fatal(err)
	}
	m := Read(path)
	if !m.HasISO || m.ISO != 3200 {
		t.Errorf("ISO = %d (has=%v), want 3200", m.ISO, m.HasISO)
	}
	if !m.HasExposure || m.ExposureMs != 30000 {
		t.Errorf("ExposureMs = %d (has=%v), want 30000", m.ExposureMs, m.HasExposure)
	}
	if m.CameraModel != "iPhone 15 Pro" {
		t.Errorf("CameraModel = %q, want %q", m.CameraModel, "iPhone 15 Pro")
	}
	if m.Orientation != 6 {
		t.Errorf("Orientation = %d, want 6", m.Orientation)
	}
	if m.Width != 4032 || m.Height != 3024 {
		t.Errorf("dims = %dx%d, want 4032x3024", m.Width, m.Height)
	}
}

func TestRead_NonTIFF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.dng")
	if err := os.WriteFile(path, []byte("not a tiff container"), 0o600); err != nil {
		t.Fatal(err)
	}
	// parseTIFF returns zero; mdls is absent in CI, so everything soft-fails to zero.
	if m := Read(path); m.HasISO || m.CameraModel != "" || m.Width != 0 {
		t.Fatalf("non-TIFF should read as empty, got %+v", m)
	}
}

// buildTIFF lays out a minimal valid TIFF with two IFDs at fixed offsets; the offset-drift asserts keep
// the hand-computed layout honest if it is ever edited.
func buildTIFF(t *testing.T) []byte {
	t.Helper()
	const (
		ifd0Off  = 8
		exifOff  = 50
		modelOff = 104
		ratOff   = 118
	)
	var b bytes.Buffer
	le := binary.LittleEndian
	w16 := func(v uint16) { _ = binary.Write(&b, le, v) }
	w32 := func(v uint32) { _ = binary.Write(&b, le, v) }
	// entry writes a 12-byte IFD entry; val is written as a LONG, whose low 2 bytes are the SHORT value.
	entry := func(tag, typ uint16, count, val uint32) {
		w16(tag)
		w16(typ)
		w32(count)
		w32(val)
	}

	b.WriteString("II")
	w16(42)
	w32(ifd0Off)

	// IFD0 (tags must ascend): Model, Orientation, Exif-pointer.
	w16(3)
	entry(tagModel, typeASCII, 14, modelOff)
	entry(tagOrientation, typeShort, 1, 6)
	entry(tagExifIFD, typeLong, 1, exifOff)
	w32(0)

	if b.Len() != exifOff {
		t.Fatalf("exif offset drift: got %d want %d", b.Len(), exifOff)
	}
	// Exif sub-IFD: ISO, ExposureTime, PixelX, PixelY.
	w16(4)
	entry(tagISOSpeed, typeShort, 1, 3200)
	entry(tagExposureTime, typeRational, 1, ratOff)
	entry(tagPixelXDimension, typeLong, 1, 4032)
	entry(tagPixelYDimension, typeLong, 1, 3024)
	w32(0)

	if b.Len() != modelOff {
		t.Fatalf("model offset drift: got %d want %d", b.Len(), modelOff)
	}
	b.WriteString("iPhone 15 Pro\x00")
	if b.Len() != ratOff {
		t.Fatalf("rational offset drift: got %d want %d", b.Len(), ratOff)
	}
	w32(30) // numerator: 30 s
	w32(1)  // denominator → 30000 ms
	return b.Bytes()
}
