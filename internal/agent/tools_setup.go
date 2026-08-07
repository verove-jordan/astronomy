package agent

import (
	"context"
	"encoding/json"
)

// registerSetupTools exposes the user's rig/site configuration and a health check (all read-only).
func registerSetupTools(r *Registry, d Deps) {
	r.Add(Tool{
		Name: "user_setup", Category: "setup",
		Description: "The user's configured rig and observing site: telescope focal length/aperture, sensor, default location (lat/lon), colour-calibration sensor, and the AI model. Use this to ground optics/location questions and as the default location for sky/weather/light-pollution tools.",
		Schema:      objectSchema(nil, map[string]any{}),
		Handler:     func(ctx context.Context, args json.RawMessage) (string, error) { return userSetup(d) },
	})
	r.Add(Tool{
		Name: "health", Category: "setup",
		Description: "Engine health: data/output/library directories.",
		Schema:      objectSchema(nil, map[string]any{}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return jsonResult(map[string]any{"data_dir": d.Cfg.DataDir, "output_dir": d.Cfg.OutputDir, "library_dir": d.Cfg.LibraryDir})
		},
	})
}

func userSetup(d Deps) (string, error) {
	c := d.Cfg
	return jsonResult(map[string]any{
		"optics": map[string]any{
			"focal_mm": c.FocalLenMM, "aperture_mm": c.ApertureMM, "pixel_um": c.PixelSizeUm,
			"sensor_w_px": c.SensorWpx, "sensor_h_px": c.SensorHpx, "barlow_x": c.BarlowX,
			"reducer_x": c.ReducerX,
			"eyepieces": c.EyepieceKit,
		},
		"site": map[string]any{
			"lat": c.LatDeg, "lon": c.LonDeg, "elevation_m": c.ElevationM, "timezone": c.Timezone,
		},
		"color_calibration": map[string]any{
			"mono_sensor": c.SpccMonoSensor, "osc_sensor": c.NightscapeOSCSensor,
		},
		"model":       c.LLMModel,
		"directories": map[string]any{"data": c.DataDir, "output": c.OutputDir, "library": c.LibraryDir},
	})
}
