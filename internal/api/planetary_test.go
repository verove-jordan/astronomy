package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/planetary"
)

// discFITSPixels builds a w×h grid with a bright filled disc on a dark background — a stand-in moon.
func discFITSPixels(w, h int) []uint16 {
	pix := make([]uint16, w*h)
	cx, cy := float64(w)/2, float64(h)/2
	rad := 0.4 * float64(w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy <= rad*rad {
				pix[y*w+x] = 60000
			} else {
				pix[y*w+x] = 400
			}
		}
	}
	return pix
}

func TestPlanetaryAlignPoints(t *testing.T) {
	data := t.TempDir()
	lights := filepath.Join(data, "moon")
	require.NoError(t, os.MkdirAll(lights, 0o755))
	fitstest.WritePixels(t, lights, "moon_L_001.fits", 128, 128, discFITSPixels(128, 128), map[string]string{
		"IMAGETYP": "'Light Frame'", "FILTER": "'L'", "EXPTIME": "0.01",
	})
	s := &Server{cfg: &config.Config{DataDir: data}, scanCache: inspect.NewScanCache()}
	h := s.Handler()

	post := func(body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/planetary/align-points", bytes.NewReader(b)))
		return rec
	}

	t.Run("estimates from the first L frame", func(t *testing.T) {
		rec := post(map[string]any{"paths": []string{lights}, "min_px": 12})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var est planetary.AlignPointsEstimate
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &est))
		assert.Equal(t, 10, est.PerAxis, "128/24 floors the per-axis density at 10")
		assert.Equal(t, 100, est.TotalPoints)
		assert.Equal(t, 100, est.SuggestedAlignPoints)
		assert.True(t, strings.HasSuffix(est.Frame, "moon_L_001.fits"), "reports the chosen frame: %s", est.Frame)
	})

	t.Run("rejects a path outside the data dir", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, post(map[string]any{"paths": []string{"/etc"}}).Code)
	})

	t.Run("no lights or videos", func(t *testing.T) {
		darks := filepath.Join(data, "darks")
		require.NoError(t, os.MkdirAll(darks, 0o755))
		fitstest.Write(t, darks, "dark_1.fits", 8, 8, 600, map[string]string{"IMAGETYP": "'Dark Frame'"})
		rec := post(map[string]any{"paths": []string{darks}})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "no light frames")
	})

	t.Run("SER is unsupported", func(t *testing.T) {
		serDir := filepath.Join(data, "ser")
		require.NoError(t, os.MkdirAll(serDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(serDir, "moon.ser"), []byte("x"), 0o644))
		rec := post(map[string]any{"paths": []string{serDir}})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "SER")
	})
}

func TestFirstLuminanceFrame(t *testing.T) {
	f := func(path, filter string, t0 int64) *inspect.Frame {
		return &inspect.Frame{Path: path, Type: inspect.Light, Filter: filter, DateObsMs: t0}
	}
	tests := []struct {
		name     string
		frames   []*inspect.Frame
		wantPath string
		wantOK   bool
	}{
		{"prefers L over R", []*inspect.Frame{f("r.fits", "R", 1), f("l.fits", "L", 5)}, "l.fits", true},
		{"canonical fallback when no L", []*inspect.Frame{f("b.fits", "B", 1), f("g.fits", "G", 2)}, "g.fits", true},
		{"chronological first within a group", []*inspect.Frame{f("late.fits", "L", 9), f("early.fits", "L", 3)}, "early.fits", true},
		{"zero timestamp sorts last", []*inspect.Frame{f("notime.fits", "L", 0), f("timed.fits", "L", 7)}, "timed.fits", true},
		{"mono group, path tiebreak", []*inspect.Frame{f("m2.fits", "", 0), f("m1.fits", "", 0)}, "m1.fits", true},
		{"no lights", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := firstLuminanceFrame(&inspect.Inventory{Frames: tt.frames})
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantPath, got.Path)
			}
		})
	}
}
