package nightscape

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestReusePhoneMaster(t *testing.T) {
	lib := t.TempDir()
	src := t.TempDir()
	// Two fake "dark" source frames.
	raws := []string{filepath.Join(src, "d1.dng"), filepath.Join(src, "d2.dng")}
	for _, p := range raws {
		if err := os.WriteFile(p, []byte("rawdata"+p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o := Options{LibraryDir: lib}

	// No sidecar yet → no reuse (would rebuild).
	if im, _ := reusePhoneMaster(o, "dark", raws); im != nil {
		t.Fatal("reused with no persisted master")
	}

	// Simulate a persisted master: write a master FITS + its signature sidecar (what persistPhoneMaster does).
	masterPath := filepath.Join(lib, "phone_master_DARK_iso3200_30000ms_4032x3024.fits")
	m := fits.NewImage(4, 4, 1)
	if err := m.WriteFITS(masterPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(masterPath+".sig", []byte(calFramesSignature("dark", raws)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unchanged source frames → reuse.
	im, note := reusePhoneMaster(o, "dark", raws)
	if im == nil {
		t.Fatal("did not reuse an unchanged master")
	}
	if im.W != 4 || im.H != 4 {
		t.Fatalf("reused wrong image %dx%d", im.W, im.H)
	}
	t.Logf("reuse note: %s", note)

	// A changed source frame → no reuse (signature differs).
	if err := os.WriteFile(raws[0], []byte("EDITED"), 0o644); err != nil {
		t.Fatal(err)
	}
	if im, _ := reusePhoneMaster(o, "dark", raws); im != nil {
		t.Fatal("reused despite an edited source frame")
	}
	// A different tag must not reuse the dark's sidecar.
	if im, _ := reusePhoneMaster(o, "bias", raws); im != nil {
		t.Fatal("bias reused a dark master")
	}
}
