package nightscape

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// fakePhoneStore is an in-memory calib.PhoneCalibStore for reuse/persist tests (no Postgres).
type fakePhoneStore struct {
	list  []calib.PhoneMaster
	saved []calib.PhoneMaster
}

func (f *fakePhoneStore) ListPhoneMasters(context.Context) ([]calib.PhoneMaster, error) {
	return f.list, nil
}

func (f *fakePhoneStore) SavePhoneMaster(_ context.Context, m calib.PhoneMaster) error {
	f.saved = append(f.saved, m)
	return nil
}

// TestBuildOrReusePhoneMaster_ReusesLibrary loads a matching library master when no cal frames are
// supplied this run — the "use them at any moment" reuse path.
func TestBuildOrReusePhoneMaster_ReusesLibrary(t *testing.T) {
	master := fits.NewImage(4, 3, 1)
	for i := range master.Pix[0] {
		master.Pix[0][i] = 0.25
	}
	path := filepath.Join(t.TempDir(), "phone_master_DARK_iso3200_30000ms_4x3.fits")
	if err := master.WriteFITS(path); err != nil {
		t.Fatal(err)
	}
	sel := &calib.PhoneMaster{Type: calib.MasterDark, ISO: 3200, ExposureMs: 30000, Width: 4, Height: 3, FrameCount: 12, Path: path}

	im, note := buildOrReusePhoneMaster(context.Background(), Options{}, "dark",
		calib.PhoneKey{ISO: 3200, ExposureMs: 30000, Width: 4, Height: 3}, sel)
	if im == nil {
		t.Fatalf("expected the reused master, got nil (note=%q)", note)
	}
	if im.W != 4 || im.H != 3 {
		t.Fatalf("reused dims = %dx%d, want 4x3", im.W, im.H)
	}
	if !strings.Contains(note, "reused from library") {
		t.Errorf("note = %q, want it to mention reuse", note)
	}
}

// TestBuildOrReusePhoneMaster_NoneAvailable returns nothing when there are no cal frames and no match.
func TestBuildOrReusePhoneMaster_NoneAvailable(t *testing.T) {
	im, note := buildOrReusePhoneMaster(context.Background(), Options{}, "dark", calib.PhoneKey{}, nil)
	if im != nil || note != "" {
		t.Fatalf(`expected (nil, ""), got (%v, %q)`, im, note)
	}
}

// TestPlanCalibration_ReadsLibrary confirms planCalibration loads the reusable library once.
func TestPlanCalibration_ReadsLibrary(t *testing.T) {
	store := &fakePhoneStore{list: []calib.PhoneMaster{{Type: calib.MasterBias, ISO: 3200}}}
	p := planCalibration(context.Background(), Options{PhoneCalib: store})
	if len(p.masters) != 1 {
		t.Fatalf("planCalibration should load the library, got %d masters", len(p.masters))
	}
}

func TestLibraryHasCandidate(t *testing.T) {
	p := calPlan{
		light:   calib.PhoneKey{ISO: 3200, CameraModel: "iPhone"},
		masters: []calib.PhoneMaster{{Type: calib.MasterDark, ISO: 3200, CameraModel: "iPhone"}},
	}
	if !libraryHasCandidate(p) {
		t.Fatal("a same-ISO/model master should be a candidate")
	}
	p.masters[0].ISO = 800
	if libraryHasCandidate(p) {
		t.Fatal("a different-ISO master should not be a candidate")
	}
}
