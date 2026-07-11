package pipeline

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestTierOf_StretchHeadroom(t *testing.T) {
	base := mode.For(mode.Deepsky)
	next := base
	next.StretchHeadroom = base.StretchHeadroom - 0.05
	if got := tierOf(base, next); got != tierB {
		t.Errorf("a stretch_headroom change should be Tier B, got %v", got)
	}
}

func TestClampPreset_StretchHeadroom(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"off preserved", 0, 0},
		{"below range clamps up", 0.5, 0.7},
		{"above range clamps down", 1.2, 1.0},
		{"in range kept", 0.85, 0.85},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := clampPreset(mode.Preset{StretchHeadroom: tt.in})
			if p.StretchHeadroom != tt.want {
				t.Errorf("clamp(%.2f) = %.3f, want %.3f", tt.in, p.StretchHeadroom, tt.want)
			}
		})
	}
}

func TestKnownParamKeys_StretchHeadroom(t *testing.T) {
	if !knownParamKeys(mode.Deepsky)["stretch_headroom"] {
		t.Error("stretch_headroom should be a known deepsky param key")
	}
}

func TestApplyParamPatch_StretchHeadroom(t *testing.T) {
	p := mode.For(mode.Deepsky)
	res, err := ApplyParamPatch(&p, json.RawMessage(`{"stretch_headroom":0.8}`))
	if err != nil {
		t.Fatalf("ApplyParamPatch: %v", err)
	}
	if p.StretchHeadroom != 0.8 {
		t.Errorf("StretchHeadroom = %.3f, want 0.8", p.StretchHeadroom)
	}
	if res.Tier != "B" {
		t.Errorf("tier = %q, want B", res.Tier)
	}
	found := false
	for _, k := range res.Changed {
		if k == "stretch_headroom" {
			found = true
		}
	}
	if !found {
		t.Errorf("changed keys %v should include stretch_headroom", res.Changed)
	}
}

// writeRGB writes a tiny RGB FITS with one bright highlight pixel and one dim pixel.
func writeRGB(t *testing.T, r, g, b []float32) string {
	t.Helper()
	im := &fits.Image{W: len(r), H: 1, C: 3, Pix: [][]float32{r, g, b}}
	path := filepath.Join(t.TempDir(), "img.fits")
	if err := im.WriteFITS(path); err != nil {
		t.Fatalf("write fits: %v", err)
	}
	return path
}

func TestApplyStretchHeadroom(t *testing.T) {
	t.Run("caps the highlight in place, keeps shadows", func(t *testing.T) {
		path := writeRGB(t, []float32{0.95, 0.10}, []float32{0.90, 0.10}, []float32{0.85, 0.10})
		note, err := applyStretchHeadroom(path, path, 0.85)
		if err != nil {
			t.Fatalf("applyStretchHeadroom: %v", err)
		}
		if note == "" {
			t.Error("expected a note when active")
		}
		im, err := fits.ReadImage(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if im.Pix[0][0] >= 0.95 {
			t.Errorf("bright core not rolled off: %.4f", im.Pix[0][0])
		}
		if im.Pix[0][1] < 0.099 || im.Pix[0][1] > 0.101 {
			t.Errorf("sub-knee shadow changed: %.4f", im.Pix[0][1])
		}
	})

	t.Run("disabled is a no-op", func(t *testing.T) {
		path := writeRGB(t, []float32{0.95}, []float32{0.90}, []float32{0.85})
		note, err := applyStretchHeadroom(path, path, 0)
		if err != nil || note != "" {
			t.Errorf("headroom 0 should be a silent no-op, got note=%q err=%v", note, err)
		}
		im, _ := fits.ReadImage(path)
		if im.Pix[0][0] != 0.95 {
			t.Errorf("file modified when disabled: %.4f", im.Pix[0][0])
		}
	})

	t.Run("writes a separate destination", func(t *testing.T) {
		src := writeRGB(t, []float32{0.95}, []float32{0.90}, []float32{0.85})
		dst := filepath.Join(t.TempDir(), "lum_lin.fits")
		if _, err := applyStretchHeadroom(src, dst, 0.85); err != nil {
			t.Fatalf("applyStretchHeadroom: %v", err)
		}
		if _, err := fits.ReadImage(dst); err != nil {
			t.Errorf("destination not written: %v", err)
		}
	})
}
