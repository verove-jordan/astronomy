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
		name     string
		query    string
		wantMode string
		wantKeys []string
	}{
		{"deepsky", "?mode=deepsky", "deepsky", []string{"color_calibration", "saturation", "stretch_headroom"}},
		{"milkyway", "?mode=milkyway", "milkyway", []string{"look", "saturation_scale", "highlight_ceiling"}},
		{"planetary", "?mode=planetary", "planetary", []string{"deconv_fwhm", "best_percent"}},
		{"comet", "?mode=comet", "comet", []string{"per_frame_starnet", "trail_mask_k"}},
		{"empty falls back to deepsky", "", "deepsky", []string{"color_calibration"}},
		{"unknown falls back to deepsky", "?mode=bogus", "deepsky", []string{"color_calibration"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mode-params"+tt.query, nil))
			require.Equal(t, http.StatusOK, rec.Code)

			var body struct {
				Mode     string         `json:"mode"`
				Defaults map[string]any `json:"defaults"`
				Menu     string         `json:"menu"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tt.wantMode, body.Mode)
			assert.NotEmpty(t, body.Menu)
			for _, k := range tt.wantKeys {
				assert.Contains(t, body.Defaults, k, "defaults should expose %q for %s", k, tt.wantMode)
			}
		})
	}
}
