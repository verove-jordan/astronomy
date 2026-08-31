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
		{"deepsky", "?mode=deepsky", "deepsky",
			[]string{"color_calibration", "saturation", "stretch_headroom", "stack_reject", "stack_combine", "stack_norm", "stack_weight"},
			[]string{"saturation", "stretch_headroom", "stack_reject_low", "stack_reject_high"}},
		{"milkyway", "?mode=milkyway", "milkyway", []string{"look", "saturation_scale", "highlight_ceiling"}, []string{"saturation_scale", "highlight_ceiling"}},
		{"planetary", "?mode=planetary", "planetary",
			[]string{"deconv_fwhm", "best_percent", "earthshine_gain", "earthshine_feather", "true_lum", "double_stack", "drizzle_scale", "shadow_lift", "align_points"},
			[]string{"best_percent", "earthshine_gain", "earthshine_feather", "drizzle_scale", "shadow_lift", "align_points"}},
		{"comet", "?mode=comet", "comet",
			[]string{"per_frame_starnet", "trail_mask_k", "stack_reject", "comet_stack_reject"},
			[]string{"trail_mask_k", "comet_stack_low", "comet_stack_high"}},
		{"sun", "?mode=sun", "sun",
			[]string{"limb_flatten", "deconv_sigma", "prominence_boost", "palette", "band", "drizzle", "window_seconds"},
			[]string{"limb_flatten", "deconv_sigma", "prominence_boost", "drizzle", "window_seconds"}},
		{"eclipse carries the solar surface plus the phase sequence", "?mode=eclipse", "eclipse",
			[]string{"two_body", "limb_flatten", "palette", "sequence_panels", "sequence_angle_deg", "sequence_spacing", "site_lat", "site_lon"},
			[]string{"sequence_panels", "sequence_angle_deg", "sequence_spacing", "site_lat", "site_lon"}},
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

// TestServer_ModeParams_StackMenu pins the algorithm catalogue the launch form's "Stacking &
// rejection" panel builds its dropdowns from. It must be served for the Siril-backed modes and
// ABSENT for the ones that stack natively — a dropdown there would offer knobs the engine ignores.
func TestServer_ModeParams_StackMenu(t *testing.T) {
	h := (&Server{}).Handler()
	read := func(t *testing.T, mode string) *struct {
		Combines []struct {
			ID      string   `json:"id"`
			Engines []string `json:"engines"`
		} `json:"combines"`
		Rejects []struct {
			ID        string   `json:"id"`
			Engines   []string `json:"engines"`
			HasParams bool     `json:"has_params"`
			Low       struct {
				Kind    string  `json:"kind"`
				Default float64 `json:"default"`
			} `json:"low"`
		} `json:"rejects"`
		Norms     []string `json:"norms"`
		Weights   []string `json:"weights"`
		AutoBands []struct {
			UpTo   int    `json:"up_to"`
			From   int    `json:"from"`
			Reject string `json:"reject"`
		} `json:"auto_bands"`
	} {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mode-params?mode="+mode, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			StackMenu *struct {
				Combines []struct {
					ID      string   `json:"id"`
					Engines []string `json:"engines"`
				} `json:"combines"`
				Rejects []struct {
					ID        string   `json:"id"`
					Engines   []string `json:"engines"`
					HasParams bool     `json:"has_params"`
					Low       struct {
						Kind    string  `json:"kind"`
						Default float64 `json:"default"`
					} `json:"low"`
				} `json:"rejects"`
				Norms     []string `json:"norms"`
				Weights   []string `json:"weights"`
				AutoBands []struct {
					UpTo   int    `json:"up_to"`
					From   int    `json:"from"`
					Reject string `json:"reject"`
				} `json:"auto_bands"`
			} `json:"stack_menu"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.StackMenu
	}

	for _, m := range []string{"deepsky", "nebula", "comet", "mosaic"} {
		t.Run(m, func(t *testing.T) {
			menu := read(t, m)
			require.NotNil(t, menu, "%s stacks with Siril and must offer the catalogue", m)
			assert.NotEmpty(t, menu.Combines)
			assert.NotEmpty(t, menu.Norms)
			assert.NotEmpty(t, menu.Weights)
			assert.Len(t, menu.AutoBands, 3, "the count-adaptive rule has three bands")

			byID := map[string]bool{}
			for _, r := range menu.Rejects {
				byID[r.ID] = true
				assert.NotEmpty(t, r.Engines, "%s must name an engine that implements it", r.ID)
				if r.HasParams {
					assert.NotEmpty(t, r.Low.Kind, "%s must say what its parameters mean", r.ID)
					assert.NotZero(t, r.Low.Default, "%s must offer a usable default", r.ID)
				}
			}
			// The panorama the panel promises: Siril's full rejection set plus the Go-only ones.
			for _, want := range []string{
				"none", "percentile", "sigma", "median_sigma", "winsorized", "linear_fit", "gesd", "mad",
				"rcr", "adaptive_weighted", "entropy_weighted",
			} {
				assert.Contains(t, byID, want, "the catalogue must offer %q", want)
			}
		})
	}
	for _, m := range []string{"planetary", "sun", "milkyway"} {
		t.Run(m+" stacks natively", func(t *testing.T) {
			assert.Nil(t, read(t, m), "%s must not offer Siril stacking knobs", m)
		})
	}
}
