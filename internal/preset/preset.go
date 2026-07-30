// Package preset holds the built-in "best params per situation" processing catalog and the shared shapes
// for user-saved presets. A preset is a situation recipe: a named, partial /api/jobs body (mode + palette
// + checkboxes + the Advanced knob JSON) the UI re-applies to the launch form. Built-ins live here in code
// (so they are validated by a test and can't drift from the engine's knob whitelist); user-saved presets
// live in Postgres (internal/store) and are merged in by the API layer. This package stays engine-free —
// it imports only encoding/json — so it can never introduce an import cycle.
package preset

import "encoding/json"

// Category groups built-in presets in the UI picker. User-saved presets carry no category (they show
// under a synthetic "my presets" group instead).
const (
	CategoryDeepsky    = "deepsky"
	CategoryNebula     = "nebula"
	CategoryNarrowband = "narrowband"
	CategorySolar      = "solar"
	CategoryComet      = "comet"
	CategoryMilkyway   = "milkyway"
)

// Payload is the situation recipe: the subset of the /api/jobs body a preset carries. Every json tag
// MUST match the run-request key so the frontend can spread it straight onto the launch form and the
// backend validates Params exactly as a real run would (pipeline.ApplyParamPatch). The *bool checkboxes
// distinguish "leave the form default" (nil) from an explicit true/false. Params is the Advanced knob
// JSON (validated per-mode on save). Input-specific fields (paths, calibration, reuse, S3, orientation)
// are intentionally absent — a preset is a recipe, not a run.
type Payload struct {
	Mode                string          `json:"mode,omitempty"`
	Format              string          `json:"format,omitempty"`
	Palette             string          `json:"palette,omitempty"`
	Look                string          `json:"look,omitempty"`
	Brightness          string          `json:"brightness,omitempty"`
	Goal                string          `json:"goal,omitempty"`
	ColorCalibration    *bool           `json:"color_calibration,omitempty"`
	Denoise             *bool           `json:"denoise,omitempty"`
	HaExcludeStars      *bool           `json:"ha_exclude_stars,omitempty"`
	DropWheelTransition *bool           `json:"drop_wheel_transition,omitempty"`
	Supervise           *bool           `json:"supervise,omitempty"`
	Params              json.RawMessage `json:"params,omitempty"`
}

// Item is the unified, API-facing preset — either a built-in (Builtin true, ID 0, Name a stable slug the
// UI translates via i18n) or a user-saved row (Builtin false, Name the user's own text, Category empty).
// Payload is carried opaquely as JSON so built-ins and DB rows share one wire shape.
type Item struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Category  string          `json:"category,omitempty"`
	Builtin   bool            `json:"builtin"`
	Favorite  bool            `json:"favorite,omitempty"` // user rows only; built-ins are never starred
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"created_at,omitempty"`
	UpdatedAt int64           `json:"updated_at,omitempty"`
}

// boolPtr is the authoring helper for the Payload checkboxes.
func boolPtr(b bool) *bool { return &b }

// mustParams marshals a knob map to raw JSON for a built-in recipe. The catalog is authored in code, so a
// marshal failure is a programming error, not a runtime condition.
func mustParams(m map[string]any) json.RawMessage {
	raw, err := json.Marshal(m)
	if err != nil {
		panic("preset: bad built-in params: " + err.Error())
	}
	return raw
}

// builtin assembles a built-in Item from a slug, category and payload recipe, marshaling the payload once.
func builtin(name, category string, p Payload) Item {
	raw, err := json.Marshal(p)
	if err != nil {
		panic("preset: bad built-in payload " + name + ": " + err.Error())
	}
	return Item{Name: name, Category: category, Builtin: true, Payload: raw}
}
