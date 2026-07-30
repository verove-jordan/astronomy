package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_ModeParams(t *testing.T) {
	h := (&Server{}).Handler() // modeParams touches no Server deps

	tests := []struct {
		name       string
		query      string
		wantMode   string
		wantKeys   []string
		wantRanges []string // numeric knobs that must carry a min/max range
	}{
		{"deepsky", "?mode=deepsky", "deepsky", []string{"color_calibration", "saturation", "stretch_headroom"}, []string{"saturation", "stretch_headroom"}},
		{"milkyway", "?mode=milkyway", "milkyway", []string{"look", "saturation_scale", "highlight_ceiling"}, []string{"saturation_scale", "highlight_ceiling"}},
		{"planetary", "?mode=planetary", "planetary",
			[]string{"deconv_fwhm", "best_percent", "earthshine_gain", "earthshine_feather", "true_lum", "double_stack", "drizzle_scale", "shadow_lift", "align_points"},
			[]string{"best_percent", "earthshine_gain", "earthshine_feather", "drizzle_scale", "shadow_lift", "align_points"}},
		{"comet", "?mode=comet", "comet", []string{"per_frame_starnet", "trail_mask_k"}, []string{"trail_mask_k"}},
		{"empty falls back to deepsky", "", "deepsky", []string{"color_calibration"}, []string{"saturation"}},
		{"unknown falls back to deepsky", "?mode=bogus", "deepsky", []string{"color_calibration"}, []string{"saturation"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mode-params"+tt.query, nil))
			require.Equal(t, http.StatusOK, rec.Code)

			var body struct {
				Mode     string         `json:"mode"`
				Defaults map[string]any `json:"defaults"`
				Ranges   map[string]struct {
					Min float64 `json:"min"`
					Max float64 `json:"max"`
					Int bool    `json:"int"`
				} `json:"ranges"`
				Menu string `json:"menu"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tt.wantMode, body.Mode)
			assert.NotEmpty(t, body.Menu)
			for _, k := range tt.wantKeys {
				assert.Contains(t, body.Defaults, k, "defaults should expose %q for %s", k, tt.wantMode)
			}
			for _, k := range tt.wantRanges {
				r, ok := body.Ranges[k]
				assert.Truef(t, ok, "ranges should expose %q for %s", k, tt.wantMode)
				assert.Lessf(t, r.Min, r.Max, "range %q should have Min < Max", k)
			}
		})
	}
}
